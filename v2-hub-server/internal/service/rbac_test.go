package service

import (
	"context"
	"errors"
	"testing"
)

// operatorFixture 准备：超管 root、角色 operator（node:read/write、admin:write）、该角色的管理员 ops。
func operatorFixture(t *testing.T) (*fixture, *AdminPrincipal, *AdminPrincipal, *Admin) {
	t.Helper()
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	root := f.login(t, "root").Principal
	if _, err := f.rbac.CreateRole(ctx, root, RoleInput{Code: "operator", Name: "运维", Permissions: []string{PermNodeRead, PermNodeWrite, PermAdminWrite}}, nil); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	opAdmin, err := f.rbac.CreateAdmin(ctx, root, AdminCreateInput{Username: "ops", Password: cred, Roles: []string{"operator"}}, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	op := f.login(t, "ops").Principal
	return f, root, op, opAdmin
}

func TestRBACEscalationRules(t *testing.T) {
	f, root, op, _ := operatorFixture(t)
	ctx := context.Background()
	rootAdminID := root.AdminID
	const weak = "short"

	tests := []struct {
		name string
		run  func() error
		want error
	}{
		{"R2 grant permission not held", func() error {
			_, err := f.rbac.CreateRole(ctx, op, RoleInput{Code: "auditor", Name: "审计", Permissions: []string{PermAuditRead}}, nil)
			return err
		}, ErrPermissionDenied},
		{"R2 grant subset ok", func() error {
			_, err := f.rbac.CreateRole(ctx, op, RoleInput{Code: "viewer", Name: "只读", Permissions: []string{PermNodeRead}}, nil)
			return err
		}, nil},
		{"R3 assign super_admin by non-super", func() error {
			_, err := f.rbac.CreateAdmin(ctx, op, AdminCreateInput{Username: "evil", Password: cred, Roles: []string{RoleSuperAdmin}}, nil)
			return err
		}, ErrPermissionDenied},
		{"R3 modify super admin by non-super", func() error {
			_, err := f.rbac.UpdateAdmin(ctx, op, rootAdminID, AdminUpdateInput{Roles: []string{}, IsDisabled: true}, nil)
			return err
		}, ErrPermissionDenied},
		{"R3 reset super admin credential by non-super", func() error {
			return f.rbac.ResetAdminPassword(ctx, op, rootAdminID, cred+"-x", nil)
		}, ErrPermissionDenied},
		{"R4 cannot disable self", func() error {
			_, err := f.rbac.UpdateAdmin(ctx, root, rootAdminID, AdminUpdateInput{Roles: []string{RoleSuperAdmin}, IsDisabled: true}, nil)
			return err
		}, ErrConflict},
		{"R6 builtin role immutable", func() error {
			roles, err := f.rbac.ListRoles(ctx)
			if err != nil {
				return err
			}
			_, err = f.rbac.UpdateRole(ctx, root, roles[0].ID, RoleInput{Name: "x", Permissions: []string{}}, nil)
			return err
		}, ErrConflict},
		{"unknown permission rejected", func() error {
			_, err := f.rbac.CreateRole(ctx, root, RoleInput{Code: "bad", Name: "x", Permissions: []string{"root:all"}}, nil)
			return err
		}, ErrInvalidArgument},
		{"duplicate username", func() error {
			_, err := f.rbac.CreateAdmin(ctx, root, AdminCreateInput{Username: "ops", Password: cred, Roles: []string{}}, nil)
			return err
		}, ErrConflict},
		{"short credential rejected", func() error {
			_, err := f.rbac.CreateAdmin(ctx, root, AdminCreateInput{Username: "shorty", Password: weak, Roles: []string{}}, nil)
			return err
		}, ErrInvalidArgument},
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

	// 还有 root 在，可以禁用 root2
	if _, err := f.rbac.UpdateAdmin(ctx, root, second.ID, AdminUpdateInput{Roles: []string{RoleSuperAdmin}, IsDisabled: true}, nil); err != nil {
		t.Fatalf("disable root2: %v", err)
	}
	// 新建 root3 后，由 root3 禁用 root：启用的超管只剩 root3，允许
	third := f.bootstrap(t, "root3")
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
	f, root, _, opAdmin := operatorFixture(t)
	ctx := context.Background()

	roles, err := f.rbac.ListRoles(ctx)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	var operatorID int64
	for _, r := range roles {
		if r.Code == "operator" {
			operatorID = r.ID
		}
		if r.Code == RoleSuperAdmin && len(r.Permissions) != len(AllPermissions) {
			t.Fatalf("super_admin should list all permissions, got %v", r.Permissions)
		}
	}
	if err := f.rbac.DeleteRole(ctx, root, operatorID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete referenced role err = %v (R6)", err)
	}
	if _, err := f.rbac.UpdateAdmin(ctx, root, opAdmin.ID, AdminUpdateInput{Roles: []string{}}, nil); err != nil {
		t.Fatalf("detach role: %v", err)
	}
	if err := f.rbac.DeleteRole(ctx, root, operatorID, nil); err != nil {
		t.Fatalf("delete role: %v", err)
	}

	page, err := f.rbac.ListAdmins(ctx, PageQuery{})
	if err != nil || page.Total != 2 {
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
