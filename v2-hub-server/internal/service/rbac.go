package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
)

// 输入约束与 hub-api.yaml 的 schema 一致。
var (
	usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,64}$`)
	roleCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)
)

const (
	minPasswordLen    = 12
	maxPasswordLen    = 128
	maxRoleNameLen    = 64
	maxRolesPerAdmin  = 16
	maxPermsPerRole   = 64
	defaultPage       = 1
	defaultPageSize   = 20
	maxPageSize       = 100
	maxPage           = 10000
	msgRoleNotFound   = "角色不存在"
	msgAdminNotFound  = "管理员不存在"
	msgLastSuperAdmin = "至少需要保留一个启用的超级管理员"
	msgRoleCodeTaken  = "角色编码已存在"
	msgOwnerNotSuper  = "所有者必须是启用状态的超级管理员"
)

// RBACService 负责管理员、角色、权限码与审计日志查询（授权规则 R1–R7、R11 见 docs/03 §4.1）。
type RBACService struct {
	store
}

// NewRBACService 创建 RBAC 服务。
func NewRBACService(pool *pgxpool.Pool) *RBACService {
	return &RBACService{store: newStore(pool)}
}

// Permission 是权限码及名称；SuperOnly 表示超管专属（Ruling-95）。
type Permission struct {
	Code      string
	Name      string
	SuperOnly bool
}

// Role 是角色及其有效权限码。
type Role struct {
	ID          int64
	Code        string
	Name        string
	IsBuiltin   bool
	Permissions []string
}

// Admin 是管理员列表项。
type Admin struct {
	ID          int64
	Username    string
	IsDisabled  bool
	TOTPEnabled bool
	// IsOwner 是否所有者（Ruling-99）。
	IsOwner     bool
	Roles       []string
	LastLoginAt *time.Time
	CreatedAt   time.Time
}

// PageQuery 是分页参数；零值表示取默认值。
type PageQuery struct {
	Page     int
	PageSize int
}

// Page 是分页结果。
type Page[T any] struct {
	Items []T
	Total int64
}

func (p PageQuery) normalize() (limit, offset int32, err error) {
	page, size := p.Page, p.PageSize
	if page == 0 {
		page = defaultPage
	}
	if size == 0 {
		size = defaultPageSize
	}
	if page < 1 || page > maxPage || size < 1 || size > maxPageSize {
		return 0, 0, newError(CodeInvalidArgument, fmt.Sprintf("page 取 1–%d，pageSize 取 1–%d", maxPage, maxPageSize))
	}
	return int32(size), int32((page - 1) * size), nil
}

// ListPermissions 返回全部权限码。
func (s *RBACService) ListPermissions(ctx context.Context) ([]Permission, error) {
	rows, err := s.q.ListPermissions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list permissions: %w", err)
	}
	out := make([]Permission, 0, len(rows))
	for _, r := range rows {
		out = append(out, Permission{Code: r.Code, Name: r.Name, SuperOnly: IsSuperOnlyPermission(r.Code)})
	}
	return out, nil
}

// ListRoles 返回全部角色；内置角色的权限取代码定义（super_admin 为全部权限码），自定义角色剔除超管专属码（Ruling-95）。
func (s *RBACService) ListRoles(ctx context.Context) ([]Role, error) {
	return listRoles(ctx, s.q)
}

func listRoles(ctx context.Context, q *sqlcgen.Queries) ([]Role, error) {
	rows, err := q.ListRoles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	permRows, err := q.ListRolePermissionCodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list role permissions: %w", err)
	}
	byRole := make(map[int64][]string, len(rows))
	for _, pr := range permRows {
		byRole[pr.RoleID] = append(byRole[pr.RoleID], pr.Code)
	}
	out := make([]Role, 0, len(rows))
	for _, r := range rows {
		out = append(out, Role{
			ID: r.ID, Code: r.Code, Name: r.Name, IsBuiltin: r.IsBuiltin,
			Permissions: rolePermissions(r.Code, r.IsBuiltin, byRole[r.ID]).Sorted(),
		})
	}
	return out, nil
}

// rolePermissions 算出一个角色的有效权限：内置角色取代码定义（库里的行不算），自定义角色取库里的行并剔除超管专属码。
func rolePermissions(code string, isBuiltin bool, stored []string) PermissionSet {
	if isBuiltin {
		if set, ok := builtinRolePermissions(code); ok {
			return set
		}
		// 代码不认识的内置角色（不该出现）一律无权限，不回退到库里的行
		return PermissionSet{}
	}
	return effectivePermissions(false, stored)
}

// RoleInput 是新建 / 修改角色的输入；Code 只在新建时使用。
type RoleInput struct {
	Code        string
	Name        string
	Permissions []string
}

func validateRoleInput(in RoleInput, isCreate bool) (PermissionSet, error) {
	if isCreate && !roleCodePattern.MatchString(in.Code) {
		return nil, newError(CodeInvalidArgument, "角色编码须为小写字母开头的 3–64 位小写字母、数字或下划线")
	}
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > maxRoleNameLen {
		return nil, newError(CodeInvalidArgument, fmt.Sprintf("角色名称长度须为 1–%d", maxRoleNameLen))
	}
	if len(in.Permissions) > maxPermsPerRole {
		return nil, newError(CodeInvalidArgument, "权限码过多")
	}
	for _, c := range in.Permissions {
		if !isKnownPermission(c) {
			return nil, newError(CodeInvalidArgument, "未知权限码: "+c)
		}
		if IsSuperOnlyPermission(c) {
			return nil, newError(CodeInvalidArgument, "超管专属权限码不能放进角色: "+c)
		}
	}
	return NewPermissionSet(in.Permissions), nil
}

// CreateRole 新建自定义角色（R2、R3）；编码不能与内置角色相同。
func (s *RBACService) CreateRole(ctx context.Context, actor *AdminPrincipal, in RoleInput, ip *netip.Addr) (*Role, error) {
	perms, err := validateRoleInput(in, true)
	if err != nil {
		return nil, err
	}
	if isBuiltinRoleCode(in.Code) {
		return nil, newError(CodeConflict, msgRoleCodeTaken)
	}
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		id, err = q.CreateRole(ctx, sqlcgen.CreateRoleParams{Code: in.Code, Name: in.Name})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgRoleCodeTaken)
		}
		if err != nil {
			return fmt.Errorf("create role %s: %w", in.Code, err)
		}
		if err := q.InsertRolePermissions(ctx, sqlcgen.InsertRolePermissionsParams{RoleID: id, Codes: perms.Sorted()}); err != nil {
			return fmt.Errorf("insert permissions of role %d: %w", id, err)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionRoleCreate, targetRole, id, ip,
			map[string]any{"code": in.Code, "name": in.Name, "permissions": perms.Sorted()}))
	})
	if err != nil {
		return nil, err
	}
	return &Role{ID: id, Code: in.Code, Name: in.Name, Permissions: perms.Sorted()}, nil
}

// UpdateRole 修改自定义角色的名称与权限（R2、R6）。
func (s *RBACService) UpdateRole(ctx context.Context, actor *AdminPrincipal, id int64, in RoleInput, ip *netip.Addr) (*Role, error) {
	perms, err := validateRoleInput(in, false)
	if err != nil {
		return nil, err
	}
	var updated *Role
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		current, err := getRole(ctx, q, id)
		if err != nil {
			return err
		}
		if current.IsBuiltin {
			return newError(CodeConflict, "内置角色不可修改")
		}
		if err := q.UpdateRoleName(ctx, sqlcgen.UpdateRoleNameParams{ID: id, Name: in.Name}); err != nil {
			return fmt.Errorf("update role %d: %w", id, err)
		}
		if err := q.DeleteRolePermissions(ctx, id); err != nil {
			return fmt.Errorf("clear permissions of role %d: %w", id, err)
		}
		if err := q.InsertRolePermissions(ctx, sqlcgen.InsertRolePermissionsParams{RoleID: id, Codes: perms.Sorted()}); err != nil {
			return fmt.Errorf("insert permissions of role %d: %w", id, err)
		}
		updated = &Role{ID: id, Code: current.Code, Name: in.Name, Permissions: perms.Sorted()}
		return writeAudit(ctx, q, adminAudit(actor, ActionRoleUpdate, targetRole, id, ip,
			map[string]any{"name": in.Name, "before": current.Permissions, "after": perms.Sorted()}))
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteRole 删除角色（R6）。
func (s *RBACService) DeleteRole(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		current, err := getRole(ctx, q, id)
		if err != nil {
			return err
		}
		if current.IsBuiltin {
			return newError(CodeConflict, "内置角色不可删除")
		}
		n, err := q.CountAdminsByRole(ctx, id)
		if err != nil {
			return fmt.Errorf("count admins of role %d: %w", id, err)
		}
		if n > 0 {
			return newError(CodeConflict, "仍有管理员使用该角色，不能删除")
		}
		if err := q.DeleteRole(ctx, id); err != nil {
			return fmt.Errorf("delete role %d: %w", id, err)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionRoleDelete, targetRole, id, ip, map[string]any{"code": current.Code}))
	})
}

// getRole 返回一个角色及其有效权限码；不存在返回 CodeNotFound。
func getRole(ctx context.Context, q *sqlcgen.Queries, id int64) (*Role, error) {
	roles, err := listRoles(ctx, q)
	if err != nil {
		return nil, err
	}
	for i := range roles {
		if roles[i].ID == id {
			return &roles[i], nil
		}
	}
	return nil, newError(CodeNotFound, msgRoleNotFound)
}

// roleCache 让同一事务里的多次角色校验只查一次全部角色；零值可用，第一次用到时才查库。
type roleCache struct {
	roles  []Role
	loaded bool
}

func (c *roleCache) get(ctx context.Context, q *sqlcgen.Queries) ([]Role, error) {
	if c.loaded {
		return c.roles, nil
	}
	roles, err := listRoles(ctx, q)
	if err != nil {
		return nil, err
	}
	c.roles, c.loaded = roles, true
	return roles, nil
}

// resolveAssignableRoles 校验角色存在且操作者有权分配（R3：super_admin 只能由超管分配或收回），返回角色 ID 列表。
func resolveAssignableRoles(ctx context.Context, q *sqlcgen.Queries, actor *AdminPrincipal, codes []string) ([]int64, error) {
	return resolveAssignableRolesCached(ctx, q, actor, codes, &roleCache{})
}

// resolveAssignableRolesCached 同 resolveAssignableRoles，全部角色经 cache 读取，供同一事务里多次校验共用。
func resolveAssignableRolesCached(ctx context.Context, q *sqlcgen.Queries, actor *AdminPrincipal, codes []string, cache *roleCache) ([]int64, error) {
	if len(codes) > maxRolesPerAdmin {
		return nil, newError(CodeInvalidArgument, "角色过多")
	}
	unique := NewPermissionSet(codes).Sorted() // 复用集合去重
	if len(unique) == 0 {
		return []int64{}, nil
	}
	all, err := cache.get(ctx, q)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(unique))
	for _, code := range unique {
		idx := slices.IndexFunc(all, func(r Role) bool { return r.Code == code })
		if idx < 0 {
			return nil, newError(CodeInvalidArgument, "角色不存在: "+code)
		}
		if err := checkCanAssign(actor, all[idx]); err != nil {
			return nil, err
		}
		ids = append(ids, all[idx].ID)
	}
	return ids, nil
}

// checkCanAssign 是 R3 的纵深防御：管理员管理接口本身只有超管能进（admin:* 超管专属），这里仍挡住非超管分配或收回 super_admin。
func checkCanAssign(actor *AdminPrincipal, role Role) error {
	if role.IsBuiltin && role.Code == RoleSuperAdmin && !actor.IsSuperAdmin {
		return newError(CodePermissionDenied, "只有超级管理员能分配或收回超级管理员角色")
	}
	return nil
}

func validatePassword(password string) error {
	if n := utf8.RuneCountInString(password); n < minPasswordLen || n > maxPasswordLen {
		return newError(CodeInvalidArgument, fmt.Sprintf("密码长度须为 %d–%d 个字符", minPasswordLen, maxPasswordLen))
	}
	return nil
}

// AdminCreateInput 是新建管理员的输入。
type AdminCreateInput struct {
	Username string
	Password string
	Roles    []string
}

// CreateAdmin 新建管理员（R3）。
func (s *RBACService) CreateAdmin(ctx context.Context, actor *AdminPrincipal, in AdminCreateInput, ip *netip.Addr) (*Admin, error) {
	if !usernamePattern.MatchString(in.Username) {
		return nil, newError(CodeInvalidArgument, "用户名须为 3–64 位字母、数字或 _ . -")
	}
	if err := validatePassword(in.Password); err != nil {
		return nil, err
	}
	hash, err := crypto.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	var created *Admin
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		roleIDs, err := resolveAssignableRoles(ctx, q, actor, in.Roles)
		if err != nil {
			return err
		}
		created, err = insertAdmin(ctx, q, in.Username, hash, roleIDs)
		if err != nil {
			return err
		}
		created.Roles = NewPermissionSet(in.Roles).Sorted()
		return writeAudit(ctx, q, adminAudit(actor, ActionAdminCreate, targetAdmin, created.ID, ip,
			map[string]any{"username": in.Username, "roles": created.Roles}))
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func insertAdmin(ctx context.Context, q *sqlcgen.Queries, username, hash string, roleIDs []int64) (*Admin, error) {
	row, err := q.CreateAdmin(ctx, sqlcgen.CreateAdminParams{Username: username, PasswordHash: hash})
	if isUniqueViolation(err) {
		return nil, newError(CodeConflict, "用户名已存在")
	}
	if err != nil {
		return nil, fmt.Errorf("create admin %s: %w", username, err)
	}
	if len(roleIDs) > 0 {
		if err := q.InsertAdminRoles(ctx, sqlcgen.InsertAdminRolesParams{AdminID: row.ID, RoleIds: roleIDs}); err != nil {
			return nil, fmt.Errorf("assign roles to admin %d: %w", row.ID, err)
		}
	}
	return &Admin{ID: row.ID, Username: username, Roles: []string{}, CreatedAt: row.CreatedAt.Time}, nil
}

// BootstrapSuperAdmin 由运维命令行创建超级管理员（无操作者，审计记为 system）；库里还没有所有者时新建的超管即所有者（Ruling-99）。
func (s *RBACService) BootstrapSuperAdmin(ctx context.Context, username, password string) (*Admin, error) {
	if !usernamePattern.MatchString(username) {
		return nil, newError(CodeInvalidArgument, "用户名须为 3–64 位字母、数字或 _ . -")
	}
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return nil, err
	}
	var created *Admin
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		// 与所有者转移共用这把锁，串行化“有没有所有者”的判断
		if err := q.LockSuperAdminRole(ctx); err != nil {
			return fmt.Errorf("lock super_admin role: %w", err)
		}
		role, err := q.GetRoleByCode(ctx, RoleSuperAdmin)
		if err != nil {
			return fmt.Errorf("get super_admin role: %w", err)
		}
		created, err = insertAdmin(ctx, q, username, hash, []int64{role.ID})
		if err != nil {
			return err
		}
		created.Roles = []string{RoleSuperAdmin}
		hasOwner, err := ownerExists(ctx, q)
		if err != nil {
			return err
		}
		if !hasOwner {
			if _, err := q.SetAdminOwner(ctx, created.ID); err != nil {
				return fmt.Errorf("set owner %d: %w", created.ID, err)
			}
			created.IsOwner = true
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorSystem, Action: ActionAdminCreate, TargetType: targetAdmin,
			TargetID: fmt.Sprint(created.ID), Detail: map[string]any{"username": username, "roles": created.Roles, "isOwner": created.IsOwner},
		})
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// ListAdmins 分页列出管理员。
func (s *RBACService) ListAdmins(ctx context.Context, pq PageQuery) (*Page[Admin], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	total, err := s.q.CountAdmins(ctx)
	if err != nil {
		return nil, fmt.Errorf("count admins: %w", err)
	}
	if total == 0 {
		return &Page[Admin]{Items: []Admin{}, Total: 0}, nil
	}
	rows, err := s.q.ListAdmins(ctx, sqlcgen.ListAdminsParams{Limit: limit, Offset: offset})
	if err != nil {
		return nil, fmt.Errorf("list admins: %w", err)
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	roleRows, err := s.q.ListRoleCodesByAdminIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list roles of admins: %w", err)
	}
	roles := make(map[int64][]string, len(rows))
	for _, rr := range roleRows {
		roles[rr.AdminID] = append(roles[rr.AdminID], rr.Code)
	}
	items := make([]Admin, 0, len(rows))
	for _, r := range rows {
		a := adminFromRow(r, roles[r.ID])
		if a.Roles == nil {
			a.Roles = []string{}
		}
		items = append(items, a)
	}
	return &Page[Admin]{Items: items, Total: total}, nil
}

// AdminUpdateInput 是修改管理员的输入。
type AdminUpdateInput struct {
	Roles      []string
	IsDisabled bool
}

// UpdateAdmin 修改管理员角色与禁用状态（R3、R4、R5、R7、R11）。
func (s *RBACService) UpdateAdmin(ctx context.Context, actor *AdminPrincipal, id int64, in AdminUpdateInput, ip *netip.Addr) (*Admin, error) {
	if id == actor.AdminID {
		return nil, newError(CodeConflict, "不能修改自己的角色或禁用自己")
	}
	var updated *Admin
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		// 先锁 super_admin 角色行，串行化 R5 的判断，避免两个并发请求各自禁用最后两名超管
		if err := q.LockSuperAdminRole(ctx); err != nil {
			return fmt.Errorf("lock super_admin role: %w", err)
		}
		target, currentRoles, err := getAdminWithRoles(ctx, q, id)
		if err != nil {
			return err
		}
		if target.IsOwner {
			return newError(CodePermissionDenied, msgOwnerProtected)
		}
		// R3：现有角色与新角色都必须是操作者可分配的，非超管因此无法改动超管
		var roles roleCache
		if _, err := resolveAssignableRolesCached(ctx, q, actor, currentRoles, &roles); err != nil {
			return err
		}
		newRoleIDs, err := resolveAssignableRolesCached(ctx, q, actor, in.Roles, &roles)
		if err != nil {
			return err
		}
		newRoles := NewPermissionSet(in.Roles).Sorted()
		losesSuper := slices.Contains(currentRoles, RoleSuperAdmin) &&
			(in.IsDisabled || !slices.Contains(newRoles, RoleSuperAdmin))
		if losesSuper {
			if err := ensureOtherSuperAdmin(ctx, q, id); err != nil {
				return err
			}
		}
		if err := q.UpdateAdminDisabled(ctx, sqlcgen.UpdateAdminDisabledParams{ID: id, IsDisabled: in.IsDisabled}); err != nil {
			return fmt.Errorf("update admin %d: %w", id, err)
		}
		if err := q.DeleteAdminRoles(ctx, id); err != nil {
			return fmt.Errorf("clear roles of admin %d: %w", id, err)
		}
		if len(newRoleIDs) > 0 {
			if err := q.InsertAdminRoles(ctx, sqlcgen.InsertAdminRolesParams{AdminID: id, RoleIds: newRoleIDs}); err != nil {
				return fmt.Errorf("assign roles to admin %d: %w", id, err)
			}
		}
		// R7：禁用后全部会话立即失效；角色变化不踢会话，下一次请求即按新权限鉴权
		if in.IsDisabled {
			if err := q.DeleteAdminSessionsByAdmin(ctx, id); err != nil {
				return fmt.Errorf("revoke sessions of admin %d: %w", id, err)
			}
		}
		target.IsDisabled = in.IsDisabled
		target.Roles = newRoles
		updated = target
		return writeAudit(ctx, q, adminAudit(actor, ActionAdminUpdate, targetAdmin, id, ip,
			map[string]any{"rolesBefore": currentRoles, "rolesAfter": newRoles, "isDisabled": in.IsDisabled}))
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// ResetAdminPassword 重置他人密码并使其全部会话失效（R3、R4、R7、R11）；改自己的密码走 AuthService.ChangeOwnPassword（R9）。
func (s *RBACService) ResetAdminPassword(ctx context.Context, actor *AdminPrincipal, id int64, password string, ip *netip.Addr) error {
	if id == actor.AdminID {
		return newError(CodeConflict, "请在账号安全页修改自己的密码")
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		target, currentRoles, err := getAdminWithRoles(ctx, q, id)
		if err != nil {
			return err
		}
		if target.IsOwner {
			return newError(CodePermissionDenied, msgOwnerProtected)
		}
		if _, err := resolveAssignableRoles(ctx, q, actor, currentRoles); err != nil {
			return err
		}
		if _, err := q.UpdateAdminPasswordHash(ctx, sqlcgen.UpdateAdminPasswordHashParams{ID: id, PasswordHash: hash}); err != nil {
			return fmt.Errorf("update password of admin %d: %w", id, err)
		}
		if err := q.DeleteAdminSessionsByAdmin(ctx, id); err != nil {
			return fmt.Errorf("revoke sessions of admin %d: %w", id, err)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionAdminPasswordReset, targetAdmin, id, ip, nil))
	})
}

func getAdminWithRoles(ctx context.Context, q *sqlcgen.Queries, id int64) (*Admin, []string, error) {
	row, err := q.GetAdminByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, newError(CodeNotFound, msgAdminNotFound)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get admin %d: %w", id, err)
	}
	roles, err := q.ListRoleCodesForAdmin(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("list roles of admin %d: %w", id, err)
	}
	slices.Sort(roles)
	// 两条查询的行结构逐字段相同，可直接转换
	a := adminFromRow(sqlcgen.ListAdminsRow(row), roles)
	return &a, roles, nil
}

// adminFromRow 把管理员行与其角色码组装成 Admin；roles 原样放入，不做 nil 处理。
func adminFromRow(r sqlcgen.ListAdminsRow, roles []string) Admin {
	return Admin{
		ID: r.ID, Username: r.Username, IsDisabled: r.IsDisabled, TOTPEnabled: r.TotpEnabled, IsOwner: r.IsOwner, Roles: roles,
		LastLoginAt: timePtr(r.LastLoginAt), CreatedAt: r.CreatedAt.Time,
	}
}

// ensureOtherSuperAdmin 实现 R5：除 excludedID 外至少还有一个启用的超管。调用方须已锁 super_admin 角色行。
func ensureOtherSuperAdmin(ctx context.Context, q *sqlcgen.Queries, excludedID int64) error {
	role, err := q.GetRoleByCode(ctx, RoleSuperAdmin)
	if err != nil {
		return fmt.Errorf("get super_admin role: %w", err)
	}
	n, err := q.CountEnabledAdminsWithRoleExcluding(ctx, sqlcgen.CountEnabledAdminsWithRoleExcludingParams{
		RoleID: role.ID, ExcludedAdminID: excludedID,
	})
	if err != nil {
		return fmt.Errorf("count super admins: %w", err)
	}
	if n == 0 {
		return newError(CodeConflict, msgLastSuperAdmin)
	}
	return nil
}

// AuditLog 是审计日志列表项。
type AuditLog struct {
	ID         int64
	ActorType  string
	ActorID    *int64
	Action     string
	TargetType *string
	TargetID   *string
	Detail     []byte // 原始 JSON
	ClientIP   *netip.Addr
	CreatedAt  time.Time
}

// ListAuditLogs 按时间倒序分页列出审计日志。
func (s *RBACService) ListAuditLogs(ctx context.Context, pq PageQuery) (*Page[AuditLog], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	total, err := s.q.CountAuditLogs(ctx)
	if err != nil {
		return nil, fmt.Errorf("count audit logs: %w", err)
	}
	if total == 0 {
		return &Page[AuditLog]{Items: []AuditLog{}, Total: 0}, nil
	}
	rows, err := s.q.ListAuditLogs(ctx, sqlcgen.ListAuditLogsParams{Limit: limit, Offset: offset})
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	items := make([]AuditLog, 0, len(rows))
	for _, r := range rows {
		item := AuditLog{ID: r.ID, ActorType: r.ActorType, Action: r.Action, Detail: r.Detail, ClientIP: r.ClientIp, CreatedAt: r.CreatedAt.Time}
		if r.ActorID.Valid {
			v := r.ActorID.Int64
			item.ActorID = &v
		}
		if r.TargetType.Valid {
			v := r.TargetType.String
			item.TargetType = &v
		}
		if r.TargetID.Valid {
			v := r.TargetID.String
			item.TargetID = &v
		}
		items = append(items, item)
	}
	return &Page[AuditLog]{Items: items, Total: total}, nil
}
