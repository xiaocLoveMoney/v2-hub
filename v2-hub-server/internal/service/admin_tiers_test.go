package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"v2-hub-server/internal/manager/totp"
)

// pw2 是改密码 / 重设密码用例里的新密码（仅测试库内有效）。
const pw2 = "another-horse-battery"

// presetHolders 是“权限码 → 持有它的预设角色”矩阵，照 docs/03 §4 预设角色表手工维护（Ruling-95）。
// 新增权限码时必须在这里登记（超管专属码登记为空），否则 TestPresetRolePermissionMatrix 失败。
var presetHolders = map[string][]string{
	PermAdminRead:        {},
	PermAdminWrite:       {},
	PermAuditRead:        {RolePartner},
	PermServerRead:       {RoleOperator, RoleOps, RolePartner},
	PermServerWrite:      {RoleOps, RolePartner},
	PermNodeRead:         {RoleOperator, RoleOps, RolePartner},
	PermNodeWrite:        {RoleOps, RolePartner},
	PermUserRead:         {RoleOperator, RoleFinance, RoleOps, RolePartner},
	PermUserWrite:        {RoleOperator, RolePartner},
	PermPlanRead:         {RoleOperator, RoleFinance, RolePartner},
	PermPlanWrite:        {RoleFinance, RolePartner},
	PermSettingRead:      {RoleOperator, RoleOps, RolePartner},
	PermSettingWrite:     {RolePartner},
	PermContentRead:      {RoleOperator, RolePartner},
	PermContentWrite:     {RoleOperator, RolePartner},
	PermTicketRead:       {RoleOperator, RolePartner},
	PermTicketWrite:      {RoleOperator, RolePartner},
	PermShopRead:         {RoleOperator, RoleFinance, RolePartner},
	PermShopWrite:        {RoleFinance, RolePartner},
	PermFinanceRead:      {RoleOperator, RoleFinance, RolePartner},
	PermFinanceWrite:     {RoleFinance, RolePartner},
	PermImpersonateWrite: {RolePartner},
	PermHostRead:         {},
	PermHostWrite:        {},
}

var presetRoleCodes = []string{RoleOperator, RoleFinance, RoleOps, RolePartner}

func TestPresetRolePermissionMatrix(t *testing.T) {
	for _, code := range AllPermissions {
		if _, ok := presetHolders[code]; !ok {
			t.Fatalf("权限码 %s 没有登记进 presetHolders 矩阵", code)
		}
	}
	if len(presetHolders) != len(AllPermissions) {
		t.Fatalf("presetHolders 里有 AllPermissions 之外的码：%d vs %d", len(presetHolders), len(AllPermissions))
	}
	for _, code := range SuperOnlyPermissions {
		if !isKnownPermission(code) {
			t.Fatalf("super-only %s not in AllPermissions", code)
		}
	}
	for _, role := range presetRoleCodes {
		got, ok := builtinRolePermissions(role)
		if !ok {
			t.Fatalf("preset %s has no code definition", role)
		}
		want := PermissionSet{}
		for code, holders := range presetHolders {
			if slices.Contains(holders, role) {
				want[code] = struct{}{}
			}
		}
		if !slices.Equal(got.Sorted(), want.Sorted()) {
			t.Fatalf("preset %s = %v, matrix says %v", role, got.Sorted(), want.Sorted())
		}
		for _, so := range SuperOnlyPermissions {
			if got.Has(so) {
				t.Fatalf("preset %s must not hold super-only %s", role, so)
			}
		}
	}
	partner, _ := builtinRolePermissions(RolePartner)
	if len(partner) != len(AllPermissions)-len(SuperOnlyPermissions) {
		t.Fatalf("partner = %v, want all minus super-only", partner.Sorted())
	}
	super, _ := builtinRolePermissions(RoleSuperAdmin)
	if len(super) != len(AllPermissions) {
		t.Fatalf("super_admin = %v", super.Sorted())
	}
	if _, ok := builtinRolePermissions(customRoleCode); ok {
		t.Fatal("custom code must not resolve to a builtin definition")
	}
}

// init.sql 的种子与代码常量逐行对应：权限码清单与顺序、内置角色集合，内置角色不留 role_permission 行。
func TestSeedMatchesCode(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	perms, err := f.rbac.ListPermissions(ctx)
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	codes := make([]string, 0, len(perms))
	for _, p := range perms {
		codes = append(codes, p.Code)
		if p.SuperOnly != IsSuperOnlyPermission(p.Code) {
			t.Fatalf("permission %s superOnly = %v", p.Code, p.SuperOnly)
		}
	}
	if !slices.Equal(codes, AllPermissions) {
		t.Fatalf("init.sql permissions = %v, code = %v", codes, AllPermissions)
	}
	rows, err := f.rbac.q.ListRoles(ctx)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	builtin := make([]string, 0, len(rows))
	builtinIDs := map[int64]bool{}
	for _, r := range rows {
		if r.IsBuiltin {
			builtin = append(builtin, r.Code)
			builtinIDs[r.ID] = true
		}
	}
	slices.Sort(builtin)
	want := slices.Clone(BuiltinRoleCodes)
	slices.Sort(want)
	if !slices.Equal(builtin, want) {
		t.Fatalf("init.sql builtin roles = %v, code = %v", builtin, want)
	}
	rp, err := f.rbac.q.ListRolePermissionCodes(ctx)
	if err != nil {
		t.Fatalf("ListRolePermissionCodes: %v", err)
	}
	for _, r := range rp {
		if builtinIDs[r.RoleID] {
			t.Fatalf("builtin role %d must not have role_permission rows", r.RoleID)
		}
	}
}

// 预设角色的权限来自代码、与自建角色取并集；库里塞进去的超管专属码不生效；不能只认编码（Ruling-95，R1）。
func TestPresetAndCustomRolePermissions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	root := f.login(t, "root").Principal
	if _, err := f.rbac.CreateRole(ctx, root, RoleInput{Code: "auditor", Name: "审计", Permissions: []string{PermAuditRead}}, nil); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if _, err := f.rbac.CreateAdmin(ctx, root, AdminCreateInput{Username: "mixed", Password: cred, Roles: []string{RoleOperator, "auditor"}}, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	p := f.login(t, "mixed").Principal
	operator, _ := builtinRolePermissions(RoleOperator)
	want := NewPermissionSet(append(operator.Sorted(), PermAuditRead))
	if !slices.Equal(p.Permissions.Sorted(), want.Sorted()) || p.IsSuperAdmin {
		t.Fatalf("mixed permissions = %v, want %v", p.Permissions.Sorted(), want.Sorted())
	}

	// 手工改库：自建角色与预设角色都塞进超管专属码与别的码
	for _, sql := range []string{
		"INSERT INTO role_permission (role_id, permission_id) SELECT r.id, p.id FROM role r, permission p WHERE r.code = 'auditor' AND p.code IN ('host:write', 'admin:write')",
		"INSERT INTO role_permission (role_id, permission_id) SELECT r.id, p.id FROM role r, permission p WHERE r.code = 'operator' AND p.code IN ('host:read', 'plan:write')",
	} {
		if _, err := f.pool.Exec(ctx, sql); err != nil {
			t.Fatalf("inject: %v", err)
		}
	}
	p = f.login(t, "mixed").Principal
	if !slices.Equal(p.Permissions.Sorted(), want.Sorted()) {
		t.Fatalf("injected rows must not take effect: %v", p.Permissions.Sorted())
	}
	roles, err := f.rbac.ListRoles(ctx)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	for _, r := range roles {
		for _, so := range SuperOnlyPermissions {
			if r.Code != RoleSuperAdmin && slices.Contains(r.Permissions, so) {
				t.Fatalf("role %s lists super-only %s", r.Code, so)
			}
		}
		if r.Code == RoleOperator && !slices.Equal(r.Permissions, operator.Sorted()) {
			t.Fatalf("operator listed as %v, want code definition", r.Permissions)
		}
	}

	// 预设角色只在 is_builtin 为真时按编码取代码定义：同编码但不是内置的行只按 role_permission 算
	if _, err := f.pool.Exec(ctx, "UPDATE role SET is_builtin = FALSE WHERE code = 'operator'"); err != nil {
		t.Fatalf("unmark builtin: %v", err)
	}
	p = f.login(t, "mixed").Principal
	if !slices.Equal(p.Permissions.Sorted(), []string{PermAuditRead, PermPlanWrite}) {
		t.Fatalf("non-builtin role named operator must not get the preset definition: %v", p.Permissions.Sorted())
	}
}

// 所有者：首个 admin-create 即所有者；三项保护（R11）；会话与列表带出标记。
func TestOwnerProtections(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := f.bootstrap(t, "root")
	second := f.bootstrap(t, "root2")
	if !owner.IsOwner || second.IsOwner {
		t.Fatalf("owner flags = %v / %v, want only the first bootstrap", owner.IsOwner, second.IsOwner)
	}
	actor := f.login(t, "root2").Principal
	if actor.IsOwner || !f.login(t, "root").Principal.IsOwner {
		t.Fatal("principal IsOwner mismatch")
	}
	_, err := f.rbac.UpdateAdmin(ctx, actor, owner.ID, AdminUpdateInput{Roles: []string{RoleSuperAdmin}, IsDisabled: true}, nil)
	wantCode(t, err, CodePermissionDenied)
	_, err = f.rbac.UpdateAdmin(ctx, actor, owner.ID, AdminUpdateInput{Roles: []string{RoleOperator}}, nil)
	wantCode(t, err, CodePermissionDenied)
	wantCode(t, f.rbac.ResetAdminPassword(ctx, actor, owner.ID, pw2, nil), CodePermissionDenied)
	wantCode(t, f.rbac.ResetAdminTOTP(ctx, actor, owner.ID, nil), CodePermissionDenied)

	page, err := f.rbac.ListAdmins(ctx, PageQuery{})
	if err != nil {
		t.Fatalf("ListAdmins: %v", err)
	}
	for _, a := range page.Items {
		if a.IsOwner != (a.ID == owner.ID) {
			t.Fatalf("list isOwner for %s = %v", a.Username, a.IsOwner)
		}
	}
}

// 命令行转移所有者：目标须是启用状态的超管；转移后旧所有者失去保护（R11）。
func TestTransferOwner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := f.bootstrap(t, "root")
	second := f.bootstrap(t, "root2")
	disabled := f.bootstrap(t, "root3")
	root := f.login(t, "root").Principal
	if _, err := f.rbac.CreateAdmin(ctx, root, AdminCreateInput{Username: "partner1", Password: cred, Roles: []string{RolePartner}}, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if _, err := f.rbac.UpdateAdmin(ctx, root, disabled.ID, AdminUpdateInput{Roles: []string{RoleSuperAdmin}, IsDisabled: true}, nil); err != nil {
		t.Fatalf("disable root3: %v", err)
	}

	_, err := f.rbac.TransferOwner(ctx, "partner1")
	wantCode(t, err, CodeConflict)
	_, err = f.rbac.TransferOwner(ctx, "root3")
	wantCode(t, err, CodeConflict)
	_, err = f.rbac.TransferOwner(ctx, "nobody")
	wantCode(t, err, CodeNotFound)

	prev, err := f.rbac.TransferOwner(ctx, "root2")
	if err != nil || prev != owner.ID {
		t.Fatalf("TransferOwner = %d, %v", prev, err)
	}
	newOwner := f.login(t, "root2").Principal
	if !newOwner.IsOwner || f.login(t, "root").Principal.IsOwner {
		t.Fatal("owner flag did not move")
	}
	// 新所有者受保护；旧所有者不再受保护
	wantCode(t, f.rbac.ResetAdminPassword(ctx, root, second.ID, pw2, nil), CodePermissionDenied)
	if _, err := f.rbac.UpdateAdmin(ctx, newOwner, owner.ID, AdminUpdateInput{Roles: []string{RoleSuperAdmin}, IsDisabled: true}, nil); err != nil {
		t.Fatalf("disable former owner: %v", err)
	}
	var actorType string
	if err := f.pool.QueryRow(ctx, "SELECT actor_type FROM audit_log WHERE action = $1", ActionAdminOwnerTransfer).Scan(&actorType); err != nil || actorType != ActorSystem {
		t.Fatalf("owner transfer audit = %q, %v", actorType, err)
	}
}

// 命令行恢复：清两步验证、重设密码都吊销目标全部会话。
func TestCLIRecovery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	res := f.login(t, "root")
	enableTOTP(t, f, res.Principal)

	if err := f.rbac.ResetAdminTOTPByUsername(ctx, "root"); err != nil {
		t.Fatalf("ResetAdminTOTPByUsername: %v", err)
	}
	if _, err := f.auth.Authenticate(ctx, res.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("session after totp reset err = %v", err)
	}
	res = f.login(t, "root") // 已不需要验证码
	if !res.Principal.TOTPSetupRequired() {
		t.Fatal("super admin without totp must be asked to set it up (R10)")
	}

	wantCode(t, f.rbac.ResetAdminPasswordByUsername(ctx, "root", "short"), CodeInvalidArgument)
	wantCode(t, f.rbac.ResetAdminPasswordByUsername(ctx, "nobody", cred), CodeNotFound)
	if err := f.rbac.ResetAdminPasswordByUsername(ctx, "root", pw2); err != nil {
		t.Fatalf("ResetAdminPasswordByUsername: %v", err)
	}
	if _, err := f.auth.Authenticate(ctx, res.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("session after password reset err = %v", err)
	}
	if _, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: pw2}); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
}

// 本人改密码（R9、R7）：旧密码错误 400 并计入频控；成功后当前会话保留、其余失效。
func TestChangeOwnPassword(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	current := f.login(t, "root")
	other := f.login(t, "root")

	wantCode(t, f.auth.ChangeOwnPassword(ctx, current.Principal, cred, "short", nil), CodeInvalidArgument)
	for i := 0; i < testAuthConfig().LoginMaxFailures; i++ {
		wantCode(t, f.auth.ChangeOwnPassword(ctx, current.Principal, bad, pw2, nil), CodeInvalidArgument)
	}
	wantCode(t, f.auth.ChangeOwnPassword(ctx, current.Principal, cred, pw2, nil), CodeRateLimited)
	f.clock.advance(16 * time.Minute)
	if err := f.auth.ChangeOwnPassword(ctx, current.Principal, cred, pw2, nil); err != nil {
		t.Fatalf("ChangeOwnPassword: %v", err)
	}
	if _, err := f.auth.Authenticate(ctx, current.Token); err != nil {
		t.Fatalf("current session should survive: %v", err)
	}
	if _, err := f.auth.Authenticate(ctx, other.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("other session err = %v, want revoked (R7)", err)
	}
	if _, err := f.auth.Login(ctx, LoginInput{Username: "root", Password: pw2}); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
	var n int
	if err := f.pool.QueryRow(ctx, "SELECT COUNT(*) FROM audit_log WHERE action = $1", ActionAdminPasswordChange).Scan(&n); err != nil || n != 1 {
		t.Fatalf("password_change audit = %d, %v", n, err)
	}
}

// 二次验证的密码路径（R12）：登录即新鲜、过窗口失效、失败计频控与审计、成功后恢复。
func TestReauthWithPassword(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	res := f.login(t, "root")
	if !res.Principal.ReauthFresh {
		t.Fatal("login must count as a fresh verification")
	}
	p, err := f.auth.Authenticate(ctx, res.Token)
	if err != nil || !p.ReauthFresh {
		t.Fatalf("fresh after login = %v, %v", p, err)
	}
	f.clock.advance(testReauthWindow + time.Second)
	if p, err = f.auth.Authenticate(ctx, res.Token); err != nil || p.ReauthFresh {
		t.Fatalf("should expire after the window: %+v, %v", p, err)
	}

	_, err = f.auth.Reauth(ctx, p, "", "", nil)
	wantCode(t, err, CodeInvalidArgument)
	_, err = f.auth.Reauth(ctx, p, bad, "", nil)
	wantCode(t, err, CodeInvalidArgument)
	until, err := f.auth.Reauth(ctx, p, cred, "", nil)
	if err != nil || !until.Equal(f.clock.now().Add(testReauthWindow)) {
		t.Fatalf("Reauth = %v, %v", until, err)
	}
	if p, err = f.auth.Authenticate(ctx, res.Token); err != nil || !p.ReauthFresh {
		t.Fatalf("fresh after reauth = %+v, %v", p, err)
	}
	// 成功一次后失败计数清零：再错满上限次数后，正确密码也被拒
	f.clock.advance(testReauthWindow + time.Second)
	for i := 0; i < testAuthConfig().LoginMaxFailures; i++ {
		_, err = f.auth.Reauth(ctx, p, bad, "", nil)
		wantCode(t, err, CodeInvalidArgument)
	}
	_, err = f.auth.Reauth(ctx, p, cred, "", nil)
	wantCode(t, err, CodeRateLimited)

	var ok, failed int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FILTER (WHERE action = $1), count(*) FILTER (WHERE action = $2) FROM audit_log",
		ActionAdminReauth, ActionAdminReauthFailed).Scan(&ok, &failed); err != nil || ok != 1 || failed != 6 {
		t.Fatalf("reauth audits ok=%d failed=%d, %v", ok, failed, err)
	}
	var detail string
	if err := f.pool.QueryRow(ctx, "SELECT detail::text FROM audit_log WHERE action = $1 LIMIT 1", ActionAdminReauthFailed).Scan(&detail); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if strings.Contains(detail, bad) || strings.Contains(detail, cred) {
		t.Fatalf("reauth audit detail must not carry the credential: %s", detail)
	}
}

// 二次验证的 TOTP 路径：开启两步验证即新鲜；只认验证码、防重放（R12）。
func TestReauthWithTOTP(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bootstrap(t, "root")
	res := f.login(t, "root")
	f.clock.advance(testReauthWindow + time.Second)
	p, err := f.auth.Authenticate(ctx, res.Token)
	if err != nil || p.ReauthFresh || !p.TOTPSetupRequired() {
		t.Fatalf("before enable = %+v, %v", p, err)
	}
	seed := enableTOTP(t, f, p)
	if p, err = f.auth.Authenticate(ctx, res.Token); err != nil || !p.ReauthFresh || p.TOTPSetupRequired() {
		t.Fatalf("enabling totp must count as fresh: %+v, %v", p, err)
	}
	// 开启时用掉的步长不能再拿来验
	_, err = f.auth.Reauth(ctx, p, "", codeFor(t, seed, f.clock.now()), nil)
	wantCode(t, err, CodeInvalidArgument)
	f.clock.advance(testReauthWindow + time.Second)
	// 已开启两步验证：只给密码不行
	_, err = f.auth.Reauth(ctx, p, cred, "", nil)
	wantCode(t, err, CodeInvalidArgument)
	code := codeFor(t, seed, f.clock.now())
	if _, err := f.auth.Reauth(ctx, p, "", code, nil); err != nil {
		t.Fatalf("Reauth with code: %v", err)
	}
	_, err = f.auth.Reauth(ctx, p, "", code, nil)
	wantCode(t, err, CodeInvalidArgument) // 同一步长重放
	f.clock.advance(totp.Period)
	if _, err := f.auth.Reauth(ctx, p, "", codeFor(t, seed, f.clock.now()), nil); err != nil {
		t.Fatalf("Reauth with next code: %v", err)
	}
}
