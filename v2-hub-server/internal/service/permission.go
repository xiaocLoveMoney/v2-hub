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
)

// AllPermissions 是全部权限码，super_admin 隐式拥有（docs/03 §4）。
var AllPermissions = []string{
	PermAdminRead, PermAdminWrite, PermAuditRead,
	PermServerRead, PermServerWrite, PermNodeRead, PermNodeWrite,
	PermUserRead, PermUserWrite, PermPlanRead, PermPlanWrite,
	PermSettingRead, PermSettingWrite, PermContentRead, PermContentWrite, PermTicketRead, PermTicketWrite,
}

// RoleSuperAdmin 是内置超级管理员角色 code。
const RoleSuperAdmin = "super_admin"

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

// Contains 报告 other 是否为 s 的子集。
func (s PermissionSet) Contains(other PermissionSet) bool {
	for c := range other {
		if !s.Has(c) {
			return false
		}
	}
	return true
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

// AdminPrincipal 是一次请求中已认证的管理员身份。
type AdminPrincipal struct {
	AdminID      int64
	Username     string
	SessionID    int64
	CSRFToken    string
	IsSuperAdmin bool
	TOTPEnabled  bool
	Permissions  PermissionSet
}

// Can 报告管理员是否拥有权限码 code（R1）。
func (p *AdminPrincipal) Can(code string) bool {
	return p.IsSuperAdmin || p.Permissions.Has(code)
}

// canGrant 报告管理员能否授予 codes 中的全部权限（R2）。
func (p *AdminPrincipal) canGrant(codes PermissionSet) bool {
	return p.IsSuperAdmin || p.Permissions.Contains(codes)
}

func effectivePermissions(isSuper bool, codes []string) PermissionSet {
	if isSuper {
		return NewPermissionSet(AllPermissions)
	}
	return NewPermissionSet(codes)
}

func isKnownPermission(code string) bool {
	return slices.Contains(AllPermissions, code)
}
