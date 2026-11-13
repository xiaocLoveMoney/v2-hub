package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
)

// 公告、教程与共享账号（docs/03 §5.9）：管理员维护、用户只读，权限码 content:*。正文是 Markdown 源文本（Ruling-25）。

// 教程平台。
const (
	PlatformWindows = "windows"
	PlatformMacOS   = "macos"
	PlatformAndroid = "android"
	PlatformIOS     = "ios"
	PlatformLinux   = "linux"
	PlatformRouter  = "router"
)

const (
	maxMarkdownLen        = 20000
	maxAnnouncementTitle  = 100
	maxTutorialClientName = 64
	maxTutorialSummary    = 255
	maxTutorialLinks      = 5
	maxTutorialLinkLabel  = 32
	maxTutorialLinkURL    = 512
	maxSortOrder          = 100000
	maxSharedName         = 64
	maxSharedCategory     = 32
	maxSharedAccount      = 255
	maxSharedSecret       = 255
	maxSharedRemark       = 255
	maxSharedPlans        = 100
	schemeHTTPSOnly       = "https"

	ActionAnnouncementCreate = "announcement.create"
	ActionAnnouncementUpdate = "announcement.update"
	ActionAnnouncementDelete = "announcement.delete"
	ActionAnnouncementMail   = "announcement.mail"
	ActionTutorialCreate     = "tutorial.create"
	ActionTutorialUpdate     = "tutorial.update"
	ActionTutorialDelete     = "tutorial.delete"
	ActionSharedCreate       = "shared_account.create"
	ActionSharedUpdate       = "shared_account.update"
	ActionSharedDelete       = "shared_account.delete"
	ActionSharedReveal       = "shared_account.reveal"
	ActionPortalSharedReveal = "portal.shared_account_reveal"
	targetAnnouncement       = "announcement"
	targetTutorial           = "tutorial"
	targetShared             = "shared_account"

	msgAnnouncementNotFound = "公告不存在"
	msgAnnouncementTitle    = "标题须为 1–100 个字符"
	msgMarkdownTooLong      = "正文过长"
	msgAnnouncementDraft    = "草稿不能群发，请先发布"
	msgAnnouncementMailed   = "这条公告已经群发过"
	msgTutorialNotFound     = "教程不存在"
	msgTutorialBadPlatform  = "平台取值不合法"
	msgTutorialBadName      = "客户端名须为 1–64 个字符"
	msgTutorialBadSummary   = "简介过长"
	msgTutorialBadLinks     = "下载链接最多 5 个，名称 1–32 个字符，地址须为 https"
	msgBadSortOrder         = "排序值须在 0–100000 之间"
	msgSharedNotFound       = "共享账号不存在"
	msgSharedBadFields      = "名称、分类、账号不能为空且不能过长"
	msgSharedSecretRequired = "请填写口令"
	msgSharedBadPlans       = "限定的套餐不存在"
	msgSharedDisabled       = "共享账号未开启"
)

var tutorialPlatforms = []string{PlatformWindows, PlatformMacOS, PlatformAndroid, PlatformIOS, PlatformLinux, PlatformRouter}

// ContentService 负责公告、教程与共享账号。
type ContentService struct {
	store
	sealer   *crypto.Sealer
	settings *SettingsService
	mail     *MailService
	now      func() time.Time
}

// NewContentService 创建内容服务；now 为 nil 时用 time.Now。
func NewContentService(pool *pgxpool.Pool, sealer *crypto.Sealer, settings *SettingsService, mailSvc *MailService, now func() time.Time) *ContentService {
	if now == nil {
		now = time.Now
	}
	return &ContentService{store: newStore(pool), sealer: sealer, settings: settings, mail: mailSvc, now: now}
}

func (s *ContentService) nowArg() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: s.now(), Valid: true}
}

func adminAudit(actor *AdminPrincipal, action, targetType string, id int64, ip *netip.Addr, detail map[string]any) AuditEntry {
	return AuditEntry{ActorType: ActorAdmin, ActorID: actor.AdminID, Action: action, TargetType: targetType, TargetID: fmt.Sprint(id), ClientIP: ip, Detail: detail}
}

func lenBetween(s string, lo, hi int) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= lo && n <= hi
}

// ---- 公告 ----------------------------------------------------------------------

// Announcement 是一条公告。PublishedAt 为 nil 表示草稿；MailedAt、CreatedAt 只在管理端有值。
type Announcement struct {
	ID          int64
	Title       string
	Content     string
	IsPinned    bool
	IsPopup     bool
	PublishedAt *time.Time
	MailedAt    *time.Time
	CreatedAt   time.Time
}

// AnnouncementInput 是新建 / 修改公告的输入。
type AnnouncementInput struct {
	Title       string
	Content     string
	IsPinned    bool
	IsPopup     bool
	IsPublished bool
}

func (in *AnnouncementInput) validate() error {
	in.Title = strings.TrimSpace(in.Title)
	if !lenBetween(in.Title, 1, maxAnnouncementTitle) {
		return newError(CodeInvalidArgument, msgAnnouncementTitle)
	}
	if utf8.RuneCountInString(in.Content) > maxMarkdownLen {
		return newError(CodeInvalidArgument, msgMarkdownTooLong)
	}
	return nil
}

// ListAnnouncements 分页列出全部公告（含草稿），新的在前。
func (s *ContentService) ListAnnouncements(ctx context.Context, pq PageQuery) (*Page[Announcement], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	total, err := s.q.CountAnnouncements(ctx)
	if err != nil {
		return nil, fmt.Errorf("count announcements: %w", err)
	}
	rows, err := s.q.ListAnnouncements(ctx, sqlcgen.ListAnnouncementsParams{RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list announcements: %w", err)
	}
	items := make([]Announcement, 0, len(rows))
	for _, r := range rows {
		items = append(items, announcementFromRow(r))
	}
	return &Page[Announcement]{Items: items, Total: total}, nil
}

func announcementFromRow(r sqlcgen.ListAnnouncementsRow) Announcement {
	return Announcement{ID: r.ID, Title: r.Title, Content: r.Content, IsPinned: r.IsPinned, IsPopup: r.IsPopup,
		PublishedAt: timePtr(r.PublishedAt), MailedAt: timePtr(r.MailedAt), CreatedAt: r.CreatedAt.Time}
}

func (s *ContentService) getAnnouncement(ctx context.Context, id int64) (*Announcement, error) {
	rows, err := s.q.ListAnnouncements(ctx, sqlcgen.ListAnnouncementsParams{ID: id, RowLimit: 1})
	if err != nil {
		return nil, fmt.Errorf("get announcement %d: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, newError(CodeNotFound, msgAnnouncementNotFound)
	}
	a := announcementFromRow(rows[0])
	return &a, nil
}

// CreateAnnouncement 新建公告；IsPublished 为真时即刻发布。错误：CodeInvalidArgument。
func (s *ContentService) CreateAnnouncement(ctx context.Context, actor *AdminPrincipal, in AnnouncementInput, ip *netip.Addr) (*Announcement, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	now := s.nowArg()
	var id int64
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		id, err = q.CreateAnnouncement(ctx, sqlcgen.CreateAnnouncementParams{
			Title: in.Title, Content: in.Content, IsPinned: in.IsPinned, IsPopup: in.IsPopup,
			PublishedAt: pgtype.Timestamptz{Time: now.Time, Valid: in.IsPublished}, CreatedBy: int8Arg(actor.AdminID), Now: now,
		})
		if err != nil {
			return fmt.Errorf("create announcement: %w", err)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionAnnouncementCreate, targetAnnouncement, id, ip, map[string]any{"title": in.Title}))
	})
	if err != nil {
		return nil, err
	}
	return s.getAnnouncement(ctx, id)
}

// UpdateAnnouncement 修改公告：由草稿改为发布时记发布时间，已发布的保持原发布时间，改回草稿则清空。
// 错误：CodeNotFound、CodeInvalidArgument。
func (s *ContentService) UpdateAnnouncement(ctx context.Context, actor *AdminPrincipal, id int64, in AnnouncementInput, ip *netip.Addr) (*Announcement, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	now := s.nowArg()
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetAnnouncementForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeNotFound, msgAnnouncementNotFound)
		}
		if err != nil {
			return fmt.Errorf("lock announcement %d: %w", id, err)
		}
		published := pgtype.Timestamptz{}
		if in.IsPublished {
			published = row.PublishedAt
			if !published.Valid {
				published = now
			}
		}
		if err := q.UpdateAnnouncement(ctx, sqlcgen.UpdateAnnouncementParams{
			ID: id, Title: in.Title, Content: in.Content, IsPinned: in.IsPinned, IsPopup: in.IsPopup, PublishedAt: published, Now: now,
		}); err != nil {
			return fmt.Errorf("update announcement %d: %w", id, err)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionAnnouncementUpdate, targetAnnouncement, id, ip, map[string]any{"isPublished": in.IsPublished}))
	})
	if err != nil {
		return nil, err
	}
	return s.getAnnouncement(ctx, id)
}

// DeleteAnnouncement 删除公告。错误：CodeNotFound。
func (s *ContentService) DeleteAnnouncement(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.DeleteAnnouncement(ctx, id)
		if err != nil {
			return fmt.Errorf("delete announcement %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgAnnouncementNotFound)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionAnnouncementDelete, targetAnnouncement, id, ip, nil))
	})
}

// MailAnnouncement 把已发布的公告群发给有已验证邮箱且未停用的用户，返回入发件箱的封数；每条公告只能群发一次。
// 错误：CodeFeatureDisabled（邮件未配置）、CodeNotFound、CodeConflict（草稿或已群发过）。
func (s *ContentService) MailAnnouncement(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) (int, error) {
	if err := s.mail.requireEnabled(); err != nil {
		return 0, err
	}
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return 0, err
	}
	queued := 0
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetAnnouncementForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeNotFound, msgAnnouncementNotFound)
		}
		if err != nil {
			return fmt.Errorf("lock announcement %d: %w", id, err)
		}
		if !row.PublishedAt.Valid {
			return newError(CodeConflict, msgAnnouncementDraft)
		}
		if row.MailedAt.Valid {
			return newError(CodeConflict, msgAnnouncementMailed)
		}
		content, err := renderMail(mailTplAnnouncement, mailVars{Site: cur.Site.Name, Title: row.Title, Body: row.Content})
		if err != nil {
			return err
		}
		emails, err := q.ListMailableUserEmails(ctx)
		if err != nil {
			return fmt.Errorf("list mailable users: %w", err)
		}
		for _, to := range emails {
			if err := s.mail.enqueue(ctx, q, to, content, mailPriorityBulk); err != nil {
				return err
			}
		}
		queued = len(emails)
		if err := q.MarkAnnouncementMailed(ctx, sqlcgen.MarkAnnouncementMailedParams{ID: id, MailedAt: s.nowArg()}); err != nil {
			return fmt.Errorf("mark announcement %d mailed: %w", id, err)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionAnnouncementMail, targetAnnouncement, id, ip, map[string]any{"queued": queued}))
	})
	if err != nil {
		return 0, err
	}
	return queued, nil
}

func publishedFromRow(r sqlcgen.ListPublishedAnnouncementsRow) Announcement {
	return Announcement{ID: r.ID, Title: r.Title, Content: r.Content, IsPinned: r.IsPinned, IsPopup: r.IsPopup, PublishedAt: timePtr(r.PublishedAt)}
}

// ListPublishedAnnouncements 分页列出已发布的公告，置顶在前、新的在前（用户端）。
func (s *ContentService) ListPublishedAnnouncements(ctx context.Context, pq PageQuery) (*Page[Announcement], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	total, err := s.q.CountPublishedAnnouncements(ctx)
	if err != nil {
		return nil, fmt.Errorf("count published announcements: %w", err)
	}
	rows, err := s.q.ListPublishedAnnouncements(ctx, sqlcgen.ListPublishedAnnouncementsParams{RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, fmt.Errorf("list published announcements: %w", err)
	}
	items := make([]Announcement, 0, len(rows))
	for _, r := range rows {
		items = append(items, publishedFromRow(r))
	}
	return &Page[Announcement]{Items: items, Total: total}, nil
}

// Highlights 返回仪表盘用的两条公告：列表里的第一条，与最新一条标了弹窗的；没有则为 nil。
func (s *ContentService) Highlights(ctx context.Context) (latest, popup *Announcement, err error) {
	pick := func(onlyPopup bool) (*Announcement, error) {
		rows, err := s.q.ListPublishedAnnouncements(ctx, sqlcgen.ListPublishedAnnouncementsParams{OnlyPopup: onlyPopup, RowLimit: 1})
		if err != nil {
			return nil, fmt.Errorf("get highlighted announcement: %w", err)
		}
		if len(rows) == 0 {
			return nil, nil
		}
		a := publishedFromRow(rows[0])
		return &a, nil
	}
	if latest, err = pick(false); err != nil {
		return nil, nil, err
	}
	if popup, err = pick(true); err != nil {
		return nil, nil, err
	}
	return latest, popup, nil
}

// ---- 教程 ----------------------------------------------------------------------

// TutorialLink 是一个下载链接。
type TutorialLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Tutorial 是一篇教程。
type Tutorial struct {
	ID          int64
	Platform    string
	ClientName  string
	Summary     string
	Links       []TutorialLink
	Content     string
	SortOrder   int
	IsPublished bool
}

// TutorialInput 是新建 / 修改教程的输入。
type TutorialInput struct {
	Platform    string
	ClientName  string
	Summary     string
	Links       []TutorialLink
	Content     string
	SortOrder   int
	IsPublished bool
}

func (in *TutorialInput) validate() error {
	in.ClientName, in.Summary = strings.TrimSpace(in.ClientName), strings.TrimSpace(in.Summary)
	if !slices.Contains(tutorialPlatforms, in.Platform) {
		return newError(CodeInvalidArgument, msgTutorialBadPlatform)
	}
	if !lenBetween(in.ClientName, 1, maxTutorialClientName) {
		return newError(CodeInvalidArgument, msgTutorialBadName)
	}
	if utf8.RuneCountInString(in.Summary) > maxTutorialSummary {
		return newError(CodeInvalidArgument, msgTutorialBadSummary)
	}
	if utf8.RuneCountInString(in.Content) > maxMarkdownLen {
		return newError(CodeInvalidArgument, msgMarkdownTooLong)
	}
	if in.SortOrder < 0 || in.SortOrder > maxSortOrder {
		return newError(CodeInvalidArgument, msgBadSortOrder)
	}
	if len(in.Links) > maxTutorialLinks {
		return newError(CodeInvalidArgument, msgTutorialBadLinks)
	}
	for i, l := range in.Links {
		l.Label, l.URL = strings.TrimSpace(l.Label), strings.TrimSpace(l.URL)
		u, err := url.Parse(l.URL)
		// 下载地址只收 https，杜绝 javascript: 等协议进到用户页面的链接里
		if err != nil || u.Scheme != schemeHTTPSOnly || u.Host == "" || len(l.URL) > maxTutorialLinkURL || !lenBetween(l.Label, 1, maxTutorialLinkLabel) {
			return newError(CodeInvalidArgument, msgTutorialBadLinks)
		}
		in.Links[i] = l
	}
	if in.Links == nil {
		in.Links = []TutorialLink{}
	}
	return nil
}

func tutorialFromRow(r sqlcgen.ListTutorialsRow) (Tutorial, error) {
	t := Tutorial{ID: r.ID, Platform: r.Platform, ClientName: r.ClientName, Summary: r.Summary, Content: r.Content,
		SortOrder: int(r.SortOrder), IsPublished: r.IsPublished, Links: []TutorialLink{}}
	if err := json.Unmarshal(r.Links, &t.Links); err != nil {
		return t, fmt.Errorf("decode links of tutorial %d: %w", r.ID, err)
	}
	return t, nil
}

// ListTutorials 列出教程，按平台与排序；onlyPublished 为真时只返回已发布的（用户端）。
func (s *ContentService) ListTutorials(ctx context.Context, onlyPublished bool) ([]Tutorial, error) {
	return s.listTutorials(ctx, sqlcgen.ListTutorialsParams{OnlyPublished: onlyPublished})
}

func (s *ContentService) listTutorials(ctx context.Context, arg sqlcgen.ListTutorialsParams) ([]Tutorial, error) {
	rows, err := s.q.ListTutorials(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("list tutorials: %w", err)
	}
	out := make([]Tutorial, 0, len(rows))
	for _, r := range rows {
		t, err := tutorialFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *ContentService) getTutorial(ctx context.Context, id int64) (*Tutorial, error) {
	list, err := s.listTutorials(ctx, sqlcgen.ListTutorialsParams{ID: id})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, newError(CodeNotFound, msgTutorialNotFound)
	}
	return &list[0], nil
}

// CreateTutorial 新建教程。错误：CodeInvalidArgument。
func (s *ContentService) CreateTutorial(ctx context.Context, actor *AdminPrincipal, in TutorialInput, ip *netip.Addr) (*Tutorial, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	links, err := json.Marshal(in.Links)
	if err != nil {
		return nil, fmt.Errorf("encode tutorial links: %w", err)
	}
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		id, err = q.CreateTutorial(ctx, sqlcgen.CreateTutorialParams{
			Platform: in.Platform, ClientName: in.ClientName, Summary: in.Summary, Links: links, Content: in.Content,
			SortOrder: int32(in.SortOrder), IsPublished: in.IsPublished, Now: s.nowArg(),
		})
		if err != nil {
			return fmt.Errorf("create tutorial: %w", err)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionTutorialCreate, targetTutorial, id, ip, map[string]any{"clientName": in.ClientName}))
	})
	if err != nil {
		return nil, err
	}
	return s.getTutorial(ctx, id)
}

// UpdateTutorial 修改教程。错误：CodeNotFound、CodeInvalidArgument。
func (s *ContentService) UpdateTutorial(ctx context.Context, actor *AdminPrincipal, id int64, in TutorialInput, ip *netip.Addr) (*Tutorial, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	links, err := json.Marshal(in.Links)
	if err != nil {
		return nil, fmt.Errorf("encode tutorial links: %w", err)
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.UpdateTutorial(ctx, sqlcgen.UpdateTutorialParams{
			ID: id, Platform: in.Platform, ClientName: in.ClientName, Summary: in.Summary, Links: links, Content: in.Content,
			SortOrder: int32(in.SortOrder), IsPublished: in.IsPublished, Now: s.nowArg(),
		})
		if err != nil {
			return fmt.Errorf("update tutorial %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgTutorialNotFound)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionTutorialUpdate, targetTutorial, id, ip, nil))
	})
	if err != nil {
		return nil, err
	}
	return s.getTutorial(ctx, id)
}

// DeleteTutorial 删除教程。错误：CodeNotFound。
func (s *ContentService) DeleteTutorial(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.DeleteTutorial(ctx, id)
		if err != nil {
			return fmt.Errorf("delete tutorial %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgTutorialNotFound)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionTutorialDelete, targetTutorial, id, ip, nil))
	})
}

// ---- 共享账号 ------------------------------------------------------------------

// SharedAccount 是一条共享账号（不含口令）。IsPublished、PlanIDs 只在管理端有意义。
type SharedAccount struct {
	ID          int64
	Name        string
	Category    string
	Account     string
	Remark      string
	SortOrder   int
	IsPublished bool
	PlanIDs     []int64
}

// SharedAccountInput 是新建 / 修改共享账号的输入。Secret 新建时必填，修改时为空表示沿用原值。
type SharedAccountInput struct {
	Name        string
	Category    string
	Account     string
	Secret      string
	Remark      string
	SortOrder   int
	IsPublished bool
	PlanIDs     []int64
}

func sharedAAD(id int64) []byte {
	return fmt.Appendf(nil, "shared_account:%d", id)
}

func (in *SharedAccountInput) validate() error {
	in.Name, in.Category, in.Account = strings.TrimSpace(in.Name), strings.TrimSpace(in.Category), strings.TrimSpace(in.Account)
	ok := lenBetween(in.Name, 1, maxSharedName) && lenBetween(in.Category, 1, maxSharedCategory) && lenBetween(in.Account, 1, maxSharedAccount) &&
		utf8.RuneCountInString(in.Secret) <= maxSharedSecret && utf8.RuneCountInString(in.Remark) <= maxSharedRemark
	if !ok {
		return newError(CodeInvalidArgument, msgSharedBadFields)
	}
	if in.SortOrder < 0 || in.SortOrder > maxSortOrder {
		return newError(CodeInvalidArgument, msgBadSortOrder)
	}
	in.PlanIDs = dedupeIDs(in.PlanIDs)
	if len(in.PlanIDs) > maxSharedPlans {
		return newError(CodeInvalidArgument, msgSharedBadPlans)
	}
	return nil
}

func sharedFromRow(r sqlcgen.ListSharedAccountsRow) SharedAccount {
	plans := r.PlanIds
	if plans == nil {
		plans = []int64{}
	}
	return SharedAccount{ID: r.ID, Name: r.Name, Category: r.Category, Account: r.Account, Remark: r.Remark, SortOrder: int(r.SortOrder),
		IsPublished: r.IsPublished, PlanIDs: plans}
}

// ListSharedAccounts 列出全部共享账号（管理端，不含口令）。
func (s *ContentService) ListSharedAccounts(ctx context.Context) ([]SharedAccount, error) {
	rows, err := s.q.ListSharedAccounts(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("list shared accounts: %w", err)
	}
	out := make([]SharedAccount, 0, len(rows))
	for _, r := range rows {
		out = append(out, sharedFromRow(r))
	}
	return out, nil
}

func (s *ContentService) getSharedAccount(ctx context.Context, id int64) (*SharedAccount, error) {
	rows, err := s.q.ListSharedAccounts(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get shared account %d: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, newError(CodeNotFound, msgSharedNotFound)
	}
	a := sharedFromRow(rows[0])
	return &a, nil
}

// saveSharedExtras 写口令（secret 非空时）与限定套餐。
func (s *ContentService) saveSharedExtras(ctx context.Context, q *sqlcgen.Queries, id int64, in SharedAccountInput) error {
	if in.Secret != "" {
		sealed, err := s.sealer.Seal([]byte(in.Secret), sharedAAD(id))
		if err != nil {
			return fmt.Errorf("seal credential of shared account %d: %w", id, err)
		}
		if err := q.SetSharedAccountCredential(ctx, sqlcgen.SetSharedAccountCredentialParams{ID: id, CredentialEnc: sealed}); err != nil {
			return fmt.Errorf("save credential of shared account %d: %w", id, err)
		}
	}
	if len(in.PlanIDs) > 0 {
		n, err := q.CountPlansByIDs(ctx, in.PlanIDs)
		if err != nil {
			return fmt.Errorf("count plans: %w", err)
		}
		if int(n) != len(in.PlanIDs) {
			return newError(CodeInvalidArgument, msgSharedBadPlans)
		}
	}
	if err := q.ClearSharedAccountPlans(ctx, id); err != nil {
		return fmt.Errorf("clear plans of shared account %d: %w", id, err)
	}
	if len(in.PlanIDs) == 0 {
		return nil
	}
	if err := q.InsertSharedAccountPlans(ctx, sqlcgen.InsertSharedAccountPlansParams{SharedAccountID: id, PlanIds: in.PlanIDs}); err != nil {
		return fmt.Errorf("set plans of shared account %d: %w", id, err)
	}
	return nil
}

// CreateSharedAccount 新建共享账号，口令加密保存。错误：CodeInvalidArgument。
func (s *ContentService) CreateSharedAccount(ctx context.Context, actor *AdminPrincipal, in SharedAccountInput, ip *netip.Addr) (*SharedAccount, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	if in.Secret == "" {
		return nil, newError(CodeInvalidArgument, msgSharedSecretRequired)
	}
	var id int64
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		id, err = q.CreateSharedAccount(ctx, sqlcgen.CreateSharedAccountParams{
			Name: in.Name, Category: in.Category, Account: in.Account, Remark: in.Remark, SortOrder: int32(in.SortOrder),
			IsPublished: in.IsPublished, Now: s.nowArg(),
		})
		if err != nil {
			return fmt.Errorf("create shared account: %w", err)
		}
		if err := s.saveSharedExtras(ctx, q, id, in); err != nil {
			return err
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionSharedCreate, targetShared, id, ip, map[string]any{"name": in.Name}))
	})
	if err != nil {
		return nil, err
	}
	return s.getSharedAccount(ctx, id)
}

// UpdateSharedAccount 修改共享账号；Secret 为空表示沿用原口令。错误：CodeNotFound、CodeInvalidArgument。
func (s *ContentService) UpdateSharedAccount(ctx context.Context, actor *AdminPrincipal, id int64, in SharedAccountInput, ip *netip.Addr) (*SharedAccount, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.UpdateSharedAccount(ctx, sqlcgen.UpdateSharedAccountParams{
			ID: id, Name: in.Name, Category: in.Category, Account: in.Account, Remark: in.Remark, SortOrder: int32(in.SortOrder),
			IsPublished: in.IsPublished, Now: s.nowArg(),
		})
		if err != nil {
			return fmt.Errorf("update shared account %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgSharedNotFound)
		}
		if err := s.saveSharedExtras(ctx, q, id, in); err != nil {
			return err
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionSharedUpdate, targetShared, id, ip, map[string]any{"secretChanged": in.Secret != ""}))
	})
	if err != nil {
		return nil, err
	}
	return s.getSharedAccount(ctx, id)
}

// DeleteSharedAccount 删除共享账号。错误：CodeNotFound。
func (s *ContentService) DeleteSharedAccount(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.DeleteSharedAccount(ctx, id)
		if err != nil {
			return fmt.Errorf("delete shared account %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgSharedNotFound)
		}
		return writeAudit(ctx, q, adminAudit(actor, ActionSharedDelete, targetShared, id, ip, nil))
	})
}

func (s *ContentService) openShared(id int64, sealed []byte) (string, error) {
	plain, err := s.sealer.Open(sealed, sharedAAD(id))
	if err != nil {
		return "", fmt.Errorf("open credential of shared account %d: %w", id, err)
	}
	return string(plain), nil
}

// RevealSharedAccount 返回共享账号的口令（管理端），读取记审计。错误：CodeNotFound。
func (s *ContentService) RevealSharedAccount(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) (string, error) {
	sealed, err := s.q.GetSharedAccountCredential(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", newError(CodeNotFound, msgSharedNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("get credential of shared account %d: %w", id, err)
	}
	if err := writeAudit(ctx, s.q, adminAudit(actor, ActionSharedReveal, targetShared, id, ip, nil)); err != nil {
		return "", err
	}
	return s.openShared(id, sealed)
}

// sharedViewer 返回用户能否看共享账号以及按哪个套餐过滤：设置开启、用户 active 且有套餐（docs/03 §5.9 共享账号）。
// 设置关闭返回 CodeFeatureDisabled；不满足条件时 planID 为 0（什么都看不到）。
func (s *ContentService) sharedViewer(ctx context.Context, p *UserPrincipal) (int64, error) {
	cur, err := s.settings.Current(ctx)
	if err != nil {
		return 0, err
	}
	if !cur.Shared.Enabled {
		return 0, newError(CodeFeatureDisabled, msgSharedDisabled)
	}
	row, err := s.q.GetUserPlanStatus(ctx, p.UserID)
	if err != nil {
		return 0, fmt.Errorf("get plan of user %d: %w", p.UserID, err)
	}
	if row.Status != UserStatusActive || !row.PlanID.Valid {
		return 0, nil
	}
	return row.PlanID.Int64, nil
}

// ListVisibleSharedAccounts 列出本人可见的共享账号（不含口令）。错误：CodeFeatureDisabled。
func (s *ContentService) ListVisibleSharedAccounts(ctx context.Context, p *UserPrincipal) ([]SharedAccount, error) {
	planID, err := s.sharedViewer(ctx, p)
	if err != nil {
		return nil, err
	}
	if planID == 0 {
		return []SharedAccount{}, nil
	}
	rows, err := s.q.ListVisibleSharedAccounts(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("list visible shared accounts: %w", err)
	}
	out := make([]SharedAccount, 0, len(rows))
	for _, r := range rows {
		out = append(out, SharedAccount{ID: r.ID, Name: r.Name, Category: r.Category, Account: r.Account, Remark: r.Remark, SortOrder: int(r.SortOrder)})
	}
	return out, nil
}

// RevealVisibleSharedAccount 返回一条本人可见的共享账号的口令，读取记审计；不可见或不存在一律 CodeNotFound（R8）。
func (s *ContentService) RevealVisibleSharedAccount(ctx context.Context, p *UserPrincipal, id int64, ip *netip.Addr) (string, error) {
	planID, err := s.sharedViewer(ctx, p)
	if err != nil {
		return "", err
	}
	if planID == 0 {
		return "", newError(CodeNotFound, msgSharedNotFound)
	}
	sealed, err := s.q.GetVisibleSharedAccountCredential(ctx, sqlcgen.GetVisibleSharedAccountCredentialParams{ID: id, PlanID: planID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", newError(CodeNotFound, msgSharedNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("get credential of shared account %d: %w", id, err)
	}
	audit := AuditEntry{ActorType: ActorUser, ActorID: p.UserID, Action: ActionPortalSharedReveal, TargetType: targetShared, TargetID: fmt.Sprint(id), ClientIP: ip}
	if p.Impersonated() {
		// 代入会话下看口令的是管理员本人，审计记实际操作的人（docs/03 §5.17 可见范围）
		audit.ActorType, audit.ActorID, audit.Detail = ActorAdmin, p.ImpersonatorAdminID, map[string]any{"asUserId": p.UserID}
	}
	if err := writeAudit(ctx, s.q, audit); err != nil {
		return "", err
	}
	return s.openShared(id, sealed)
}
