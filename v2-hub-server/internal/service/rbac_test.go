package service

import (
	"context"
	"errors"
	"testing"
)

// customRoleCode 是用例里自建角色的编码（不能与内置角色撞名）。
const customRoleCode = "node_editor"

// rbacFixture 准备：超管 root（所有者）、超管 root2、自建角色 node_editor（node:read/write）及其管理员 ops、
// 持预设角色 partner 的管理员 partner1。返回 root、root2、partner1 的身份与 ops 的记录。
type rbacFixture struct {
	*fixture
	root, root2, partner *AdminPrincipal
	ops                  *Admin
}

func newRBACFixture(t *testing.T) *rbacFixture {
	t.Helper()
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	f.bootstrap(t, "root2")
	root := f.login(t, "root").Principal
	if _, err := f.rbac.CreateRole(ctx, root, RoleInput{Code: customRoleCode, Name: "节点编辑", Permissions: []string{PermNodeRead, PermNodeWrite}}, nil); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	opAdmin, err := f.rbac.CreateAdmin(ctx, root, AdminCreateInput{Username: "ops", Password: cred, Roles: []string{customRoleCode}}, nil)
	if err != nil {
		t.Fatalf("CreateAdmin ops: %v", err)
	}
	if _, err := f.rbac.CreateAdmin(ctx, root, AdminCreateInput{Username: "partner1", Password: cred, Roles: []string{RolePartner}}, nil); err != nil {
		t.Fatalf("CreateAdmin partner1: %v", err)
	}
	return &rbacFixture{
		fixture: f, root: root, root2: f.login(t, "root2").Principal, partner: f.login(t, "partner1").Principal, ops: opAdmin,
	}
}

func TestRBACEscalationRules(t *testing.T) {
	f := newRBACFixture(t)
	ctx := context.Background()
	const weak = "short"
	roleID := func(code string) int64 {
		roles, err := f.rbac.ListRoles(ctx)
		if err != nil {
			t.Fatalf("ListRoles: %v", err)
		}
		for _, r := range roles {
			if r.Code == code {
				return r.ID
			}
		}
		t.Fatalf("role %s not found", code)
		return 0
	}

	type testCase struct {
		name string
		run  func() error
		want error
	}
	tests := []testCase{
		{"R2 update role with super-only permission", func() error {
			_, err := f.rbac.UpdateRole(ctx, f.root, roleID(customRoleCode), RoleInput{Name: "x", Permissions: []string{PermNodeRead, PermHostRead}}, nil)
			return err
		}, ErrInvalidArgument},
		{"R3 assign super_admin by non-super", func() error {
			_, err := f.rbac.CreateAdmin(ctx, f.partner, AdminCreateInput{Username: "evil", Password: cred, Roles: []string{RoleSuperAdmin}}, nil)
			return err
		}, ErrPermissionDenied},
		{"R3 modify super admin by non-super", func() error {
			_, err := f.rbac.UpdateAdmin(ctx, f.partner, f.root2.AdminID, AdminUpdateInput{Roles: []string{}, IsDisabled: true}, nil)
			return err
		}, ErrPermissionDenied},
		{"R3 reset super admin credential by non-super", func() error {
			return f.rbac.ResetAdminPassword(ctx, f.partner, f.root2.AdminID, cred+"-x", nil)
		}, ErrPermissionDenied},
		{"R4 cannot disable self", func() error {
			_, err := f.rbac.UpdateAdmin(ctx, f.root2, f.root2.AdminID, AdminUpdateInput{Roles: []string{RoleSuperAdmin}, IsDisabled: true}, nil)
			return err
		}, ErrConflict},
		{"R4 cannot reset own password via admin management", func() error {
			return f.rbac.ResetAdminPassword(ctx, f.root2, f.root2.AdminID, cred+"-x", nil)
		}, ErrConflict},
		{"unknown permission rejected", func() error {
			_, err := f.rbac.CreateRole(ctx, f.root, RoleInput{Code: "bad", Name: "x", Permissions: []string{"root:all"}}, nil)
			return err
		}, ErrInvalidArgument},
		{"duplicate username", func() error {
			_, err := f.rbac.CreateAdmin(ctx, f.root, AdminCreateInput{Username: "ops", Password: cred, Roles: []string{}}, nil)
			return err
		}, ErrConflict},
		{"short credential rejected", func() error {
			_, err := f.rbac.CreateAdmin(ctx, f.root, AdminCreateInput{Username: "shorty", Password: weak, Roles: []string{}}, nil)
			return err
		}, ErrInvalidArgument},
	}
	// R2：任何一个超管专属码都不能放进新角色
	for _, code := range SuperOnlyPermissions {
		tests = append(tests, testCase{"R2 create role with " + code, func() error {
			_, err := f.rbac.CreateRole(ctx, f.root, RoleInput{Code: "with_super_only", Name: "x", Permissions: []string{PermUserRead, code}}, nil)
			return err
		}, ErrInvalidArgument})
	}
	// R6：内置角色不可改不可删；也不能新建同编码的角色
	for _, code := range BuiltinRoleCodes {
		tests = append(tests,
			testCase{"R6 builtin " + code + " immutable", func() error {
				_, err := f.rbac.UpdateRole(ctx, f.root, roleID(code), RoleInput{Name: "x", Permissions: []string{}}, nil)
				return err
			}, ErrConflict},
			testCase{"R6 builtin " + code + " undeletable", func() error {
				return f.rbac.DeleteRole(ctx, f.root, roleID(code), nil)
			}, ErrConflict},
			testCase{"builtin code " + code + " not reusable", func() error {
				_, err := f.rbac.CreateRole(ctx, f.root, RoleInput{Code: code, Name: "x", Permissions: []string{}}, nil)
				return err
			}, ErrConflict},
		)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if tt.want == nil && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestLastSuperAdminProtected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	second := f.bootstrap(t, "root2")
	root := f.login(t, "root").Principal
	// 有所有者时它永远是一个启用的超管，R5 碰不到；这里清掉所有者，模拟没有所有者的存量库来单独校验 R5
	if _, err := f.pool.Exec(ctx, "UPDATE admin SET is_owner = FALSE"); err != nil {
		t.Fatalf("clear owner: %v", err)
	}

	// 还有 root 在，可以禁用 root2
	if _, err := f.rbac.UpdateAdmin(ctx, root, second.ID, AdminUpdateInput{Roles: []string{RoleSuperAdmin}, IsDisabled: true}, nil); err != nil {
		t.Fatalf("disable root2: %v", err)
	}
	// 新建 root3 后，由 root3 禁用 root：启用的超管只剩 root3，允许
	third := f.bootstrap(t, "root3")
	if _, err := f.pool.Exec(ctx, "UPDATE admin SET is_owner = FALSE"); err != nil {
		t.Fatalf("clear owner: %v", err)
	}
	root3 := f.login(t, "root3").Principal
	if _, err := f.rbac.UpdateAdmin(ctx, root3, root.AdminID, AdminUpdateInput{Roles: []string{RoleSuperAdmin}, IsDisabled: true}, nil); err != nil {
		t.Fatalf("disable root: %v", err)
	}
	// 现在撤掉 root3 会违反 R5（R4 已挡住 root3 自降，这里直接校验规则函数）
	if err := ensureOtherSuperAdmin(ctx, f.rbac.q, third.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("ensureOtherSuperAdmin err = %v, want conflict (R5)", err)
	}
	// root3 再恢复 root：允许
	if _, err := f.rbac.UpdateAdmin(ctx, root3, root.AdminID, AdminUpdateInput{Roles: []string{RoleSuperAdmin}}, nil); err != nil {
		t.Fatalf("enable root: %v", err)
	}
	if err := ensureOtherSuperAdmin(ctx, f.rbac.q, third.ID); err != nil {
		t.Fatalf("with root enabled, R5 should pass: %v", err)
	}
}

func TestRoleLifecycleAndAudit(t *testing.T) {
	f := newRBACFixture(t)
	ctx := context.Background()

	roles, err := f.rbac.ListRoles(ctx)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	var customID int64
	for _, r := range roles {
		if r.Code == customRoleCode {
			customID = r.ID
		}
		if r.Code == RoleSuperAdmin && len(r.Permissions) != len(AllPermissions) {
			t.Fatalf("super_admin should list all permissions, got %v", r.Permissions)
		}
	}
	if err := f.rbac.DeleteRole(ctx, f.root, customID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete referenced role err = %v (R6)", err)
	}
	if _, err := f.rbac.UpdateAdmin(ctx, f.root, f.ops.ID, AdminUpdateInput{Roles: []string{}}, nil); err != nil {
		t.Fatalf("detach role: %v", err)
	}
	if err := f.rbac.DeleteRole(ctx, f.root, customID, nil); err != nil {
		t.Fatalf("delete role: %v", err)
	}

	page, err := f.rbac.ListAdmins(ctx, PageQuery{})
	if err != nil || page.Total != 4 {
		t.Fatalf("ListAdmins = %+v, %v", page, err)
	}
	if _, err := f.rbac.ListAdmins(ctx, PageQuery{PageSize: 1000}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("oversized page err = %v", err)
	}
	logs, err := f.rbac.ListAuditLogs(ctx, PageQuery{PageSize: 100})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	seen := make(map[string]bool, len(logs.Items))
	for _, l := range logs.Items {
		seen[l.Action] = true
	}
	for _, want := range []string{ActionAdminCreate, ActionAdminLogin, ActionRoleCreate, ActionRoleDelete, ActionAdminUpdate} {
		if !seen[want] {
			t.Fatalf("audit log missing %s; got %v", want, seen)
		}
	}
}
