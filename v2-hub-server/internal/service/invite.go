package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
)

// 邀请码（docs/03 §5.8）：每个用户一个个人码，管理员可另发带次数与到期时间的码。

// 邀请码种类。
const (
	InviteKindAdmin = "admin"
	InviteKindUser  = "user"
	// InviteKindAgent 是代理的码，经它注册的用户归该代理（Ruling-102）。
	InviteKindAgent = "agent"
)

const (
	inviteCodeLen = 10
	// inviteAlphabet 去掉易混的 0 O 1 I L（docs/03 §6）
	inviteAlphabet     = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	inviteGenRetries   = 5
	inviteeListLimit   = 100
	maxInviteCodeUses  = 100000
	maxInviteRemarkLen = 255
	inviteMaskKeep     = 2
	inviteMask         = "***"
	// 个人码每小时最多重置 5 次
	inviteResetsPerHour = 5
	guardKeyInviteReset = "invite-reset:"

	ActionInviteCreate = "invite.create"
	ActionInviteUpdate = "invite.update"
	ActionInviteDelete = "invite.delete"
	ActionInviteReset  = "portal.invite_reset"
	targetInvite       = "invite_code"

	msgInviteNotFound     = "邀请码不存在"
	msgInviteBadKind      = "kind 只能是 admin、user、agent"
	msgInviteBadUses      = "可用次数须在 0–100000 之间"
	msgInviteRemarkLong   = "备注过长"
	msgInvitePersonalOnly = "个人邀请码不能删除，只能停用"
	msgInviteUserDisabled = "个人邀请码未开启"
	msgInviteResetOften   = "重置过于频繁，请稍后再试"
)

var inviteCodePattern = regexp.MustCompile(`^[2-9A-HJKMNP-Z]{10}$`)

// normalizeInviteCode 返回大写的邀请码；形状不对时返回空串（不必查库）。
func normalizeInviteCode(raw string) string {
	v := strings.ToUpper(strings.TrimSpace(raw))
	if !inviteCodePattern.MatchString(v) {
		return ""
	}
	return v
}

// randomString 见 crypto.RandomString；返回的 error 由调用方加上下文。
func randomString(alphabet string, n int) (string, error) {
	return crypto.RandomString(alphabet, n)
}

func randomInviteCode() (string, error) {
	code, err := randomString(inviteAlphabet, inviteCodeLen)
	if err != nil {
		return "", fmt.Errorf("generate invite code: %w", err)
	}
	return code, nil
}

// inviteUse 是注册时用掉的一个邀请码：CodeID 邀请码 id；InviterID 邀请人用户 id（管理员码与代理码为 0）；
// AgentID 是新用户应归属的代理（代理码的代理、或个人码主人所属的代理，Ruling-102），0 表示平台用户。
type inviteUse struct {
	CodeID    int64
	InviterID int64
	AgentID   int64
}

// useInviteCode 在注册事务里校验邀请码并把已用次数加一（docs/03 §5.8、§5.23）。
// 行已加锁，计数与上限的判断不会被并发注册穿透。无效、停用、过期、用尽一律 CodeInvalidArgument，提示不区分。
// 新用户会归到某个代理时，再锁住该代理的配额行（锁顺序：邀请码 → 代理配额行），名下未删除用户数已达上限即按邀请码不可用处理；
// 代理码另要求代理处于启用状态（Ruling-102、Ruling-103）。
func (s *AccountService) useInviteCode(ctx context.Context, q *sqlcgen.Queries, cur Settings, code string) (inviteUse, error) {
	row, err := q.GetInviteCodeForUpdate(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return inviteUse{}, newError(CodeInvalidArgument, msgInviteInvalid)
	}
	if err != nil {
		return inviteUse{}, fmt.Errorf("get invite code: %w", err)
	}
	maxUses := int(row.MaxUses)
	personal := row.OwnerUserID.Valid
	if personal {
		maxUses = cur.Invite.UserMaxUses
	}
	usable := !row.IsDisabled && !row.IsOwnerInactive &&
		(!personal || cur.Invite.UserEnabled) &&
		(!row.ExpiresAt.Valid || row.ExpiresAt.Time.After(s.now())) &&
		(maxUses == 0 || int(row.UsedCount) < maxUses)
	if !usable {
		return inviteUse{}, newError(CodeInvalidArgument, msgInviteInvalid)
	}
	use := inviteUse{CodeID: row.ID, InviterID: row.OwnerUserID.Int64}
	agentCode := row.AgentID.Valid
	switch {
	case agentCode:
		// 经代理码注册的用户没有邀请人、不产生返利（Ruling-102）
		use.AgentID, use.InviterID = row.AgentID.Int64, 0
	case row.OwnerAgentID.Valid:
		use.AgentID = row.OwnerAgentID.Int64
	default:
	}
	if use.AgentID != 0 {
		if err := checkAgentAcceptsSignup(ctx, q, use.AgentID, agentCode); err != nil {
			return inviteUse{}, err
		}
	}
	if err := q.BumpInviteCodeUse(ctx, row.ID); err != nil {
		return inviteUse{}, fmt.Errorf("bump invite code %d: %w", row.ID, err)
	}
	return use, nil
}

// checkAgentAcceptsSignup 锁住代理的配额行并判断还能不能经邀请码再多一个名下用户（docs/03 §5.23 注册）：
// 名下未删除用户数已达上限即不可用；requireEnabled 为真（代理码）时代理还须处于启用状态。不可用一律按邀请码无效处理。
func checkAgentAcceptsSignup(ctx context.Context, q *sqlcgen.Queries, agentID int64, requireEnabled bool) error {
	profile, err := q.LockAgentProfile(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return newError(CodeInvalidArgument, msgInviteInvalid)
	}
	if err != nil {
		return fmt.Errorf("lock agent profile %d: %w", agentID, err)
	}
	if requireEnabled && profile.IsDisabled {
		return newError(CodeInvalidArgument, msgInviteInvalid)
	}
	n, err := q.CountAgentUsers(ctx, int8Arg(agentID))
	if err != nil {
		return fmt.Errorf("count users of agent %d: %w", agentID, err)
	}
	if n >= int64(profile.MaxUsers) {
		return newError(CodeInvalidArgument, msgInviteInvalid)
	}
	return nil
}

// Invitee 是被邀请人（用户名已打码）。
type Invitee struct {
	Username  string
	CreatedAt time.Time
}

// InviteInfo 是本人的邀请码与邀请情况。
type InviteInfo struct {
	// Enabled：设置里开了个人邀请码，且本人的码未被停用。
	Enabled   bool
	Code      string
	MaxUses   int
	UsedCount int
	Invitees  []Invitee
	// 返利（docs/03 §5.16）：RebatePercent 为 0 表示未开启；Rebates 是最近 100 条，被邀请人用户名已打码。
	RebatePercent int
	RebateTotal   int64
	Rebates       []InviteRebate
}

// InviteRebate 是一条返利记录（用户名已打码）。
type InviteRebate struct {
	Username  string
	Amount    int64
	CreatedAt time.Time
}

func maskUsername(name string) string {
	r := []rune(name)
	if len(r) > inviteMaskKeep {
		r = r[:inviteMaskKeep]
	}
	return string(r) + inviteMask
}

// GetInvite 返回本人的邀请码与最近 100 个被邀请人；首次查看时生成个人码。设置关闭时 Enabled 为 false、Code 为空。
func (s *AccountService) GetInvite(ctx context.Context, p *UserPrincipal) (*InviteInfo, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return nil, err
	}
	info := &InviteInfo{MaxUses: cur.Invite.UserMaxUses, Invitees: []Invitee{}, RebatePercent: cur.Rebate.Percent, Rebates: []InviteRebate{}}
	if info.RebateTotal, err = s.q.SumRebateByInviter(ctx, int8Arg(p.UserID)); err != nil {
		return nil, fmt.Errorf("sum rebate of user %d: %w", p.UserID, err)
	}
	rebates, err := s.q.ListRebatesByInviter(ctx, sqlcgen.ListRebatesByInviterParams{InviterID: int8Arg(p.UserID), Limit: inviteeListLimit})
	if err != nil {
		return nil, fmt.Errorf("list rebates of user %d: %w", p.UserID, err)
	}
	for _, r := range rebates {
		info.Rebates = append(info.Rebates, InviteRebate{Username: maskUsername(r.InviteeUsername), Amount: r.Amount, CreatedAt: r.CreatedAt.Time})
	}
	rows, err := s.q.ListInvitees(ctx, sqlcgen.ListInviteesParams{InvitedBy: int8Arg(p.UserID), Limit: inviteeListLimit})
	if err != nil {
		return nil, fmt.Errorf("list invitees of user %d: %w", p.UserID, err)
	}
	for _, r := range rows {
		info.Invitees = append(info.Invitees, Invitee{Username: maskUsername(r.Username), CreatedAt: r.CreatedAt.Time})
	}
	if !cur.Invite.UserEnabled {
		return info, nil
	}
	row, err := s.ensureUserInviteCode(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	info.UsedCount = int(row.UsedCount)
	if !row.IsDisabled {
		info.Enabled, info.Code = true, row.Code
	}
	return info, nil
}

func (s *AccountService) ensureUserInviteCode(ctx context.Context, userID int64) (sqlcgen.GetUserInviteCodeRow, error) {
	for range inviteGenRetries {
		row, err := s.q.GetUserInviteCode(ctx, int8Arg(userID))
		if err == nil {
			return row, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return row, fmt.Errorf("get invite code of user %d: %w", userID, err)
		}
		code, err := randomInviteCode()
		if err != nil {
			return row, err
		}
		// 并发的首次查看或随机码撞了别人的：ON CONFLICT DO NOTHING，下一轮重读或换码
		if err := s.q.EnsureUserInviteCode(ctx, sqlcgen.EnsureUserInviteCodeParams{Code: code, OwnerUserID: int8Arg(userID)}); err != nil {
			return row, fmt.Errorf("create invite code of user %d: %w", userID, err)
		}
	}
	return sqlcgen.GetUserInviteCodeRow{}, fmt.Errorf("create invite code of user %d: gave up after %d collisions", userID, inviteGenRetries)
}

// ResetInvite 重置本人的邀请码：旧码作废，已用次数不清零。
// 错误：CodeFeatureDisabled（设置未开启）、CodeRateLimited。
func (s *AccountService) ResetInvite(ctx context.Context, p *UserPrincipal, ip *netip.Addr) (*InviteInfo, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return nil, err
	}
	if !cur.Invite.UserEnabled {
		return nil, newError(CodeFeatureDisabled, msgInviteUserDisabled)
	}
	guardKey := guardKeyInviteReset + fmt.Sprint(p.UserID)
	if s.guards.inviteReset.IsLocked(guardKey) {
		return nil, newError(CodeRateLimited, msgInviteResetOften)
	}
	s.guards.inviteReset.RecordFailure(guardKey)
	if _, err := s.ensureUserInviteCode(ctx, p.UserID); err != nil {
		return nil, err
	}
	for range inviteGenRetries {
		code, err := randomInviteCode()
		if err != nil {
			return nil, err
		}
		err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
			if _, err := q.ResetUserInviteCode(ctx, sqlcgen.ResetUserInviteCodeParams{OwnerUserID: int8Arg(p.UserID), Code: code}); err != nil {
				return err
			}
			return writeAudit(ctx, q, portalAudit(p, ActionInviteReset, ip))
		})
		if err == nil {
			return s.GetInvite(ctx, p)
		}
		if !isUniqueViolation(err) {
			return nil, fmt.Errorf("reset invite code of user %d: %w", p.UserID, err)
		}
	}
	return nil, fmt.Errorf("reset invite code of user %d: gave up after %d collisions", p.UserID, inviteGenRetries)
}

// InviteCode 是管理端看到的一条邀请码。
type InviteCode struct {
	ID            int64
	Code          string
	Kind          string
	OwnerUserID   *int64
	OwnerUsername string
	// AgentID / AgentUsername 是代理码的代理（Ruling-102），其余为 nil / 空串。
	AgentID       *int64
	AgentUsername string
	MaxUses       int
	UsedCount     int
	ExpiresAt     *time.Time
	IsDisabled    bool
	Remark        string
	CreatedAt     time.Time
}

// InviteCodeInput 是新建 / 修改邀请码的输入；个人码只认 IsDisabled。
type InviteCodeInput struct {
	MaxUses    int
	ExpiresAt  *time.Time
	Remark     string
	IsDisabled bool
}

func (in InviteCodeInput) validate() error {
	if in.MaxUses < 0 || in.MaxUses > maxInviteCodeUses {
		return newError(CodeInvalidArgument, msgInviteBadUses)
	}
	if utf8.RuneCountInString(in.Remark) > maxInviteRemarkLen {
		return newError(CodeInvalidArgument, msgInviteRemarkLong)
	}
	return nil
}

func (s *AccountService) inviteFromRow(r sqlcgen.ListInviteCodesRow, cur Settings) InviteCode {
	c := InviteCode{ID: r.ID, Code: r.Code, Kind: InviteKindAdmin, OwnerUsername: r.OwnerUsername, MaxUses: int(r.MaxUses), UsedCount: int(r.UsedCount),
		ExpiresAt: timePtr(r.ExpiresAt), IsDisabled: r.IsDisabled, Remark: r.Remark, CreatedAt: r.CreatedAt.Time}
	if r.OwnerUserID.Valid {
		id := r.OwnerUserID.Int64
		c.Kind, c.OwnerUserID, c.MaxUses = InviteKindUser, &id, cur.Invite.UserMaxUses
	}
	if r.AgentID.Valid {
		id := r.AgentID.Int64
		c.Kind, c.AgentID, c.AgentUsername = InviteKindAgent, &id, r.AgentUsername
	}
	return c
}

// ListInviteCodes 分页列出邀请码；kind 为空表示全部，agentID 非 0 时只看该代理的码（Ruling-102）。
func (s *AccountService) ListInviteCodes(ctx context.Context, kind string, agentID int64, pq PageQuery) (*Page[InviteCode], error) {
	if kind != "" && kind != InviteKindAdmin && kind != InviteKindUser && kind != InviteKindAgent {
		return nil, newError(CodeInvalidArgument, msgInviteBadKind)
	}
	if agentID < 0 {
		return nil, newError(CodeInvalidArgument, msgBadAgentFilter)
	}
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return nil, err
	}
	total, err := s.q.CountInviteCodes(ctx, sqlcgen.CountInviteCodesParams{Kind: kind, AgentID: agentID})
	if err != nil {
		return nil, fmt.Errorf("count invite codes: %w", err)
	}
	rows, err := s.q.ListInviteCodes(ctx, sqlcgen.ListInviteCodesParams{Kind: kind, AgentID: agentID, RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list invite codes: %w", err)
	}
	items := make([]InviteCode, 0, len(rows))
	for _, r := range rows {
		items = append(items, s.inviteFromRow(r, cur))
	}
	return &Page[InviteCode]{Items: items, Total: total}, nil
}

func (s *AccountService) getInviteCode(ctx context.Context, id int64) (*InviteCode, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListInviteCodes(ctx, sqlcgen.ListInviteCodesParams{ID: id, RowLimit: 1})
	if err != nil {
		return nil, fmt.Errorf("get invite code %d: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, newError(CodeNotFound, msgInviteNotFound)
	}
	c := s.inviteFromRow(rows[0], cur)
	return &c, nil
}

// CreateInviteCode 新建管理员邀请码，码由服务端生成。错误：CodeInvalidArgument。
func (s *AccountService) CreateInviteCode(ctx context.Context, actor *AdminPrincipal, in InviteCodeInput, ip *netip.Addr) (*InviteCode, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	for range inviteGenRetries {
		code, err := randomInviteCode()
		if err != nil {
			return nil, err
		}
		var id int64
		err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
			if id, err = q.CreateAdminInviteCode(ctx, sqlcgen.CreateAdminInviteCodeParams{
				Code: code, MaxUses: int32(in.MaxUses), ExpiresAt: optionalTime(in.ExpiresAt), IsDisabled: in.IsDisabled, Remark: in.Remark,
				CreatedBy: int8Arg(actor.AdminID),
			}); err != nil {
				return err
			}
			return writeAudit(ctx, q, adminAudit(actor, ActionInviteCreate, targetInvite, id, ip, map[string]any{"maxUses": in.MaxUses}))
		})
		if err == nil {
			return s.getInviteCode(ctx, id)
		}
		if !isUniqueViolation(err) {
			return nil, fmt.Errorf("create invite code: %w", err)
		}
	}
	return nil, fmt.Errorf("create invite code: gave up after %d collisions", inviteGenRetries)
}

// UpdateInviteCode 修改邀请码：管理员码改次数、到期、备注与停用；个人码只改停用。错误：CodeNotFound、CodeInvalidArgument。
func (s *AccountService) UpdateInviteCode(ctx context.Context, actor *AdminPrincipal, id int64, in InviteCodeInput, ip *netip.Addr) (*InviteCode, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		owner, err := q.GetInviteCodeOwner(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeNotFound, msgInviteNotFound)
		}
		if err != nil {
			return fmt.Errorf("lock invite code %d: %w", id, err)
		}
		if owner.Valid {
			err = q.SetInviteCodeDisabled(ctx, sqlcgen.SetInviteCodeDisabledParams{ID: id, IsDisabled: in.IsDisabled})
		} else {
			err = q.UpdateAdminInviteCode(ctx, sqlcgen.UpdateAdminInviteCodeParams{
				ID: id, MaxUses: int32(in.MaxUses), ExpiresAt: optionalTime(in.ExpiresAt), IsDisabled: in.IsDisabled, Remark: in.Remark,
			})
		}
		if err != nil {
			return fmt.Errorf("update invite code %d: %w", id, err)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionInviteUpdate, targetInvite, id, ip, map[string]any{"isDisabled": in.IsDisabled}))
	})
	if err != nil {
		return nil, err
	}
	return s.getInviteCode(ctx, id)
}

// DeleteInviteCode 删除管理员邀请码。错误：CodeNotFound、CodeConflict（个人码不可删）。
func (s *AccountService) DeleteInviteCode(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		owner, err := q.GetInviteCodeOwner(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeNotFound, msgInviteNotFound)
		}
		if err != nil {
			return fmt.Errorf("lock invite code %d: %w", id, err)
		}
		if owner.Valid {
			return newError(CodeConflict, msgInvitePersonalOnly)
		}
		if err := q.DeleteInviteCode(ctx, id); err != nil {
			return fmt.Errorf("delete invite code %d: %w", id, err)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionInviteDelete, targetInvite, id, ip, nil))
	})
}
