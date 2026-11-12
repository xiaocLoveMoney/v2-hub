package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

// AdminManager 是 Web 层需要的 RBAC 能力（由 service.RBACService 实现）。
type AdminManager interface {
	ListPermissions(ctx context.Context) ([]service.Permission, error)
	ListRoles(ctx context.Context) ([]service.Role, error)
	CreateRole(ctx context.Context, actor *service.AdminPrincipal, in service.RoleInput, ip *netip.Addr) (*service.Role, error)
	UpdateRole(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.RoleInput, ip *netip.Addr) (*service.Role, error)
	DeleteRole(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	ListAdmins(ctx context.Context, pq service.PageQuery) (*service.Page[service.Admin], error)
	CreateAdmin(ctx context.Context, actor *service.AdminPrincipal, in service.AdminCreateInput, ip *netip.Addr) (*service.Admin, error)
	UpdateAdmin(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.AdminUpdateInput, ip *netip.Addr) (*service.Admin, error)
	ResetAdminPassword(ctx context.Context, actor *service.AdminPrincipal, id int64, password string, ip *netip.Addr) error
	ListAuditLogs(ctx context.Context, pq service.PageQuery) (*service.Page[service.AuditLog], error)
}

// ---- VO：字段与 hub-api.yaml 的 schema 一致 ----

type permissionVO struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type roleVO struct {
	ID          int64    `json:"id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	IsBuiltin   bool     `json:"isBuiltin"`
	Permissions []string `json:"permissions"`
}

type adminVO struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	IsDisabled  bool       `json:"isDisabled"`
	Roles       []string   `json:"roles"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type auditLogVO struct {
	ID         int64           `json:"id"`
	ActorType  string          `json:"actorType"`
	ActorID    *int64          `json:"actorId"`
	Action     string          `json:"action"`
	TargetType *string         `json:"targetType"`
	TargetID   *string         `json:"targetId"`
	Detail     json.RawMessage `json:"detail"`
	ClientIP   *string         `json:"clientIp"`
	CreatedAt  time.Time       `json:"createdAt"`
}

type pageVO[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}

func toRoleVO(r service.Role) roleVO {
	return roleVO{ID: r.ID, Code: r.Code, Name: r.Name, IsBuiltin: r.IsBuiltin, Permissions: r.Permissions}
}

func toAdminVO(a service.Admin) adminVO {
	return adminVO{ID: a.ID, Username: a.Username, IsDisabled: a.IsDisabled, Roles: a.Roles, LastLoginAt: a.LastLoginAt, CreatedAt: a.CreatedAt}
}

func toAuditLogVO(l service.AuditLog) auditLogVO {
	vo := auditLogVO{
		ID: l.ID, ActorType: l.ActorType, ActorID: l.ActorID, Action: l.Action,
		TargetType: l.TargetType, TargetID: l.TargetID, CreatedAt: l.CreatedAt,
	}
	if len(l.Detail) > 0 {
		vo.Detail = l.Detail
	}
	if l.ClientIP != nil {
		s := l.ClientIP.String()
		vo.ClientIP = &s
	}
	return vo
}

func mapSlice[S, T any](in []S, f func(S) T) []T {
	out := make([]T, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}

// ---- 请求体 ----

type roleRequest struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

type adminCreateRequest struct {
	Username   string   `json:"username"`
	Credential string   `json:"password"`
	Roles      []string `json:"roles"`
}

type adminUpdateRequest struct {
	Roles      []string `json:"roles"`
	IsDisabled bool     `json:"isDisabled"`
}

type credentialRequest struct {
	Credential string `json:"password"`
}

// ---- 处理函数 ----

func (h *handlers) listPermissions(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	perms, err := h.rbac.ListPermissions(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(perms, func(p service.Permission) permissionVO { return permissionVO{Code: p.Code, Name: p.Name} }))
}

func (h *handlers) listRoles(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	roles, err := h.rbac.ListRoles(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mapSlice(roles, toRoleVO))
}

func (h *handlers) createRole(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req roleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	role, err := h.rbac.CreateRole(r.Context(), p, service.RoleInput{Code: req.Code, Name: req.Name, Permissions: req.Permissions}, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toRoleVO(*role))
}

func (h *handlers) updateRole(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req roleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	role, err := h.rbac.UpdateRole(r.Context(), p, id, service.RoleInput{Name: req.Name, Permissions: req.Permissions}, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toRoleVO(*role))
}

func (h *handlers) deleteRole(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.rbac.DeleteRole(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) listAdmins(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.rbac.ListAdmins(r.Context(), pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, pageVO[adminVO]{Items: mapSlice(page.Items, toAdminVO), Total: page.Total})
}

func (h *handlers) createAdmin(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req adminCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in := service.AdminCreateInput{Username: req.Username, Roles: req.Roles}
	pw := req.Credential
	in.Password = pw
	admin, err := h.rbac.CreateAdmin(r.Context(), p, in, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAdminVO(*admin))
}

func (h *handlers) updateAdmin(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req adminUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	admin, err := h.rbac.UpdateAdmin(r.Context(), p, id, service.AdminUpdateInput{Roles: req.Roles, IsDisabled: req.IsDisabled}, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAdminVO(*admin))
}

func (h *handlers) resetAdminCredential(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req credentialRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.rbac.ResetAdminPassword(r.Context(), p, id, req.Credential, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) listAuditLogs(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.rbac.ListAuditLogs(r.Context(), pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, pageVO[auditLogVO]{Items: mapSlice(page.Items, toAuditLogVO), Total: page.Total})
}
