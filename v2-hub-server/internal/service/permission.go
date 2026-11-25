package service

import (
	"slices"
	"sort"
)

// 权限码与 docs/03 §4 权限码表、contract/deploy/init.sql 种子逐行对应。
const (
	PermAdminRead   = "admin:read"
	PermAdminWrite  = "admin:write"
	PermAuditRead   = "audit:read"
	PermServerRead  = "server:read"
	PermServerWrite = "server:write"
	PermNodeRead    = "node:read"
	PermNodeWrite   = "node:write"
	PermUserRead    = "user:read"
	PermUserWrite   = "user:write"
	PermPlanRead    = "plan:read"
	PermPlanWrite   = "plan:write"

	PermSettingRead  = "setting:read"
	PermSettingWrite = "setting:write"
	PermContentRead  = "content:read"
	PermContentWrite = "content:write"
	PermTicketRead   = "ticket:read"
	PermTicketWrite  = "ticket:write"
	// M5：商店与钱包（docs/03 §4）
	PermShopRead         = "shop:read"
	PermShopWrite        = "shop:write"
	PermFinanceRead      = "finance:read"
	PermFinanceWrite     = "finance:write"
	PermImpersonateWrite = "impersonate:write"
	// M11：SSH 托管（docs/03 §5.22）
	PermHostRead  = "host:read"
	PermHostWrite = "host:write"
)

// AllPermissions 是全部权限码，super_admin 隐式拥有（docs/03 §4）。
var AllPermissions = []string{
	PermAdminRead, PermAdminWrite, PermAuditRead,
	PermServerRead, PermServerWrite, PermNodeRead, PermNodeWrite,
	PermUserRead, PermUserWrite, PermPlanRead, PermPlanWrite,
	PermSettingRead, PermSettingWrite, PermContentRead, PermContentWrite, PermTicketRead, PermTicketWrite,
	PermShopRead, PermShopWrite, PermFinanceRead, PermFinanceWrite, PermImpersonateWrite,
	PermHostRead, PermHostWrite,
}

// SuperOnlyPermissions 是超管专属权限码：只有 super_admin 拥有，任何其它角色都不能包含，库里即使有行也不生效（Ruling-95，R1 / R2）。
var SuperOnlyPermissions = []string{PermAdminRead, PermAdminWrite, PermHostRead, PermHostWrite}

// 内置角色 code（docs/03 §4 预设角色表，Ruling-95）。
const (
	// RoleSuperAdmin 是内置超级管理员角色，隐式拥有全部权限码。
	RoleSuperAdmin = "super_admin"
	// RoleOperator 客服运营、RoleFinance 财务、RoleOps 运维、RolePartner 合伙人：预设角色，权限由代码定义、不存 role_permission。
	RoleOperator = "operator"
	RoleFinance  = "finance"
	RoleOps      = "ops"
	RolePartner  = "partner"
)

// BuiltinRoleCodes 是全部内置角色 code，与 init.sql 里 is_builtin 的角色行逐行对应。
var BuiltinRoleCodes = []string{RoleSuperAdmin, RoleOperator, RoleFinance, RoleOps, RolePartner}

// presetRolePermissions 是显式列出权限码的预设角色；partner 按全集计算，不在此表（docs/03 §4）。
var presetRolePermissions = map[string][]string{
	RoleOperator: {
		PermUserRead, PermUserWrite, PermTicketRead, PermTicketWrite, PermContentRead, PermContentWrite,
		PermPlanRead, PermShopRead, PermServerRead, PermNodeRead, PermFinanceRead, PermSettingRead,
	},
	RoleFinance: {
		PermFinanceRead, PermFinanceWrite, PermShopRead, PermShopWrite, PermPlanRead, PermPlanWrite, PermUserRead,
	},
	RoleOps: {
		PermServerRead, PermServerWrite, PermNodeRead, PermNodeWrite, PermUserRead, PermSettingRead,
	},
}

// IsSuperOnlyPermission 报告 code 是否为超管专属权限码。
func IsSuperOnlyPermission(code string) bool {
	return slices.Contains(SuperOnlyPermissions, code)
}

// isBuiltinRoleCode 报告 code 是否为内置角色的编码；自定义角色不能用这些编码。
func isBuiltinRoleCode(code string) bool {
	return slices.Contains(BuiltinRoleCodes, code)
}

// builtinRolePermissions 返回内置角色由代码定义的权限码集合；code 不是内置角色时 ok 为 false。
// 调用方须先确认角色行的 is_builtin 为真，不能只凭编码（Ruling-95）。
func builtinRolePermissions(code string) (set PermissionSet, ok bool) {
	switch code {
	case RoleSuperAdmin:
		return NewPermissionSet(AllPermissions), true
	case RolePartner:
		return withoutSuperOnly(AllPermissions), true
	default:
		codes, found := presetRolePermissions[code]
		if !found {
			return nil, false
		}
		return withoutSuperOnly(codes), true
	}
}

// withoutSuperOnly 由权限码列表构造集合并剔除超管专属码。
func withoutSuperOnly(codes []string) PermissionSet {
	set := make(PermissionSet, len(codes))
	for _, c := range codes {
		if !IsSuperOnlyPermission(c) {
			set[c] = struct{}{}
		}
	}
	return set
}

// PermissionSet 是权限码集合。
type PermissionSet map[string]struct{}

// NewPermissionSet 由权限码列表构造集合（自动去重）。
func NewPermissionSet(codes []string) PermissionSet {
	set := make(PermissionSet, len(codes))
	for _, c := range codes {
		set[c] = struct{}{}
	}
	return set
}

// Has 报告集合是否包含 code。
func (s PermissionSet) Has(code string) bool {
	_, ok := s[code]
	return ok
}

// Sorted 返回排好序的权限码列表。
func (s PermissionSet) Sorted() []string {
	out := make([]string, 0, len(s))
	for c := range s {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// 管理端账号类型（admin.kind，Ruling-101）：创建后不可改。
const (
	AdminKindStaff = "staff"
	AdminKindAgent = "agent"
)

// AdminPrincipal 是一次请求中已认证的管理员身份。
type AdminPrincipal struct {
	AdminID  int64
	Username string
	// Kind 是账号类型：AdminKindStaff 员工 / AdminKindAgent 代理（Ruling-101）。代理的 Permissions 恒为空、IsSuperAdmin 恒为假。
	Kind         string
	SessionID    int64
	CSRFToken    string
	IsSuperAdmin bool
	// IsOwner 是否所有者（Ruling-99）。
	IsOwner     bool
	TOTPEnabled bool
	// ReauthFresh 表示本会话的二次验证仍在有效期内（R12）；由 service 层按会话的 reauth_until 与时钟算好，Web 层不碰时钟。
	ReauthFresh bool
	Permissions PermissionSet
}

// Can 报告管理员是否拥有权限码 code（R1）；代理一律没有（Ruling-101）。
func (p *AdminPrincipal) Can(code string) bool {
	if p.IsAgent() {
		return false
	}
	return p.IsSuperAdmin || p.Permissions.Has(code)
}

// IsAgent 报告该账号是二级代理（Ruling-101）：只能访问本人接口与代理专用接口。
func (p *AdminPrincipal) IsAgent() bool {
	return p.Kind == AdminKindAgent
}

// TOTPSetupRequired 报告该管理员是尚未开启两步验证的超管，除豁免接口外都应被拦下（R10）。
func (p *AdminPrincipal) TOTPSetupRequired() bool {
	return p.IsSuperAdmin && !p.TOTPEnabled
}

// effectivePermissions 算出有效权限：超管为全集；其余剔除超管专属码（R1，运行时兜底，覆盖手工改库）。
func effectivePermissions(isSuper bool, codes []string) PermissionSet {
	if isSuper {
		return NewPermissionSet(AllPermissions)
	}
	return withoutSuperOnly(codes)
}

func isKnownPermission(code string) bool {
	return slices.Contains(AllPermissions, code)
}
