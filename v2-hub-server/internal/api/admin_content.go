package api

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

// 公告、教程、共享账号接口（docs/03 §5.9），与 hub-api.yaml 的 admin-content 及用户侧对应路径逐条对应。

// ContentManager 是内容接口需要的业务能力（由 service.ContentService 实现）。
type ContentManager interface {
	ListAnnouncements(ctx context.Context, pq service.PageQuery) (*service.Page[service.Announcement], error)
	CreateAnnouncement(ctx context.Context, actor *service.AdminPrincipal, in service.AnnouncementInput, ip *netip.Addr) (*service.Announcement, error)
	UpdateAnnouncement(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.AnnouncementInput, ip *netip.Addr) (*service.Announcement, error)
	DeleteAnnouncement(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	MailAnnouncement(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) (int, error)
	ListPublishedAnnouncements(ctx context.Context, pq service.PageQuery) (*service.Page[service.Announcement], error)
	Highlights(ctx context.Context) (latest, popup *service.Announcement, err error)

	ListTutorials(ctx context.Context, onlyPublished bool) ([]service.Tutorial, error)
	CreateTutorial(ctx context.Context, actor *service.AdminPrincipal, in service.TutorialInput, ip *netip.Addr) (*service.Tutorial, error)
	UpdateTutorial(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.TutorialInput, ip *netip.Addr) (*service.Tutorial, error)
	DeleteTutorial(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error

	ListSharedAccounts(ctx context.Context) ([]service.SharedAccount, error)
	CreateSharedAccount(ctx context.Context, actor *service.AdminPrincipal, in service.SharedAccountInput, ip *netip.Addr) (*service.SharedAccount, error)
	UpdateSharedAccount(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.SharedAccountInput, ip *netip.Addr) (*service.SharedAccount, error)
	DeleteSharedAccount(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	RevealSharedAccount(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) (string, error)
	ListVisibleSharedAccounts(ctx context.Context, p *service.UserPrincipal) ([]service.SharedAccount, error)
	RevealVisibleSharedAccount(ctx context.Context, p *service.UserPrincipal, id int64, ip *netip.Addr) (string, error)
}

// ---- 公告 ----

// announcementBody 对应 hub-api.yaml AnnouncementInput。
type announcementBody struct {
	Title       string `json:"title"`
	Content     string `json:"content"`
	IsPinned    bool   `json:"isPinned"`
	IsPopup     bool   `json:"isPopup"`
	IsPublished bool   `json:"isPublished"`
}

func (b announcementBody) input() service.AnnouncementInput {
	return service.AnnouncementInput{Title: b.Title, Content: b.Content, IsPinned: b.IsPinned, IsPopup: b.IsPopup, IsPublished: b.IsPublished}
}

// announcementVO 对应 hub-api.yaml Announcement；mailedAt、createdAt 只在管理端输出。
type announcementVO struct {
	ID          int64      `json:"id"`
	Title       string     `json:"title"`
	Content     string     `json:"content"`
	IsPinned    bool       `json:"isPinned"`
	IsPopup     bool       `json:"isPopup"`
	PublishedAt *time.Time `json:"publishedAt"`
	MailedAt    *time.Time `json:"mailedAt,omitempty"`
	CreatedAt   *time.Time `json:"createdAt,omitempty"`
}

func toAnnouncementVO(a service.Announcement, forAdmin bool) announcementVO {
	vo := announcementVO{ID: a.ID, Title: a.Title, Content: a.Content, IsPinned: a.IsPinned, IsPopup: a.IsPopup, PublishedAt: a.PublishedAt}
	if forAdmin {
		created := a.CreatedAt
		vo.MailedAt, vo.CreatedAt = a.MailedAt, &created
	}
	return vo
}

func optionalAnnouncementVO(a *service.Announcement) *announcementVO {
	if a == nil {
		return nil
	}
	vo := toAnnouncementVO(*a, false)
	return &vo
}

type announcementPageVO struct {
	Items []announcementVO `json:"items"`
	Total int64            `json:"total"`
}

func toAnnouncementPageVO(page *service.Page[service.Announcement], forAdmin bool) announcementPageVO {
	out := announcementPageVO{Items: make([]announcementVO, 0, len(page.Items)), Total: page.Total}
	for _, a := range page.Items {
		out.Items = append(out.Items, toAnnouncementVO(a, forAdmin))
	}
	return out
}

func (h *handlers) listAnnouncements(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.content.ListAnnouncements(r.Context(), pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAnnouncementPageVO(page, true))
}

func (h *handlers) createAnnouncement(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req announcementBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.content.CreateAnnouncement(r.Context(), p, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAnnouncementVO(*a, true))
}

func (h *handlers) updateAnnouncement(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req announcementBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.content.UpdateAnnouncement(r.Context(), p, id, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAnnouncementVO(*a, true))
}

func (h *handlers) deleteAnnouncement(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.content.DeleteAnnouncement(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

type mailBatchVO struct {
	Queued int `json:"queued"`
}

func (h *handlers) mailAnnouncement(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	queued, err := h.content.MailAnnouncement(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, mailBatchVO{Queued: queued})
}

func (h *handlers) listUserAnnouncements(w http.ResponseWriter, r *http.Request, _ *service.UserPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.content.ListPublishedAnnouncements(r.Context(), pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toAnnouncementPageVO(page, false))
}

// ---- 教程 ----

type tutorialLinkVO struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// tutorialBody 对应 hub-api.yaml TutorialInput。
type tutorialBody struct {
	Platform    string           `json:"platform"`
	ClientName  string           `json:"clientName"`
	Summary     string           `json:"summary"`
	Links       []tutorialLinkVO `json:"links"`
	Content     string           `json:"content"`
	SortOrder   int              `json:"sortOrder"`
	IsPublished bool             `json:"isPublished"`
}

func (b tutorialBody) input() service.TutorialInput {
	links := make([]service.TutorialLink, 0, len(b.Links))
	for _, l := range b.Links {
		links = append(links, service.TutorialLink{Label: l.Label, URL: l.URL})
	}
	return service.TutorialInput{Platform: b.Platform, ClientName: b.ClientName, Summary: b.Summary, Links: links, Content: b.Content,
		SortOrder: b.SortOrder, IsPublished: b.IsPublished}
}

// tutorialVO 对应 hub-api.yaml Tutorial。
type tutorialVO struct {
	ID int64 `json:"id"`
	tutorialBody
}

func toTutorialVO(t service.Tutorial) tutorialVO {
	links := make([]tutorialLinkVO, 0, len(t.Links))
	for _, l := range t.Links {
		links = append(links, tutorialLinkVO{Label: l.Label, URL: l.URL})
	}
	return tutorialVO{ID: t.ID, tutorialBody: tutorialBody{Platform: t.Platform, ClientName: t.ClientName, Summary: t.Summary, Links: links,
		Content: t.Content, SortOrder: t.SortOrder, IsPublished: t.IsPublished}}
}

func (h *handlers) writeTutorials(w http.ResponseWriter, r *http.Request, onlyPublished bool) {
	list, err := h.content.ListTutorials(r.Context(), onlyPublished)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]tutorialVO, 0, len(list))
	for _, t := range list {
		out = append(out, toTutorialVO(t))
	}
	writeOK(w, out)
}

func (h *handlers) listTutorials(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	h.writeTutorials(w, r, false)
}

func (h *handlers) listUserTutorials(w http.ResponseWriter, r *http.Request, _ *service.UserPrincipal) {
	h.writeTutorials(w, r, true)
}

func (h *handlers) createTutorial(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req tutorialBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	t, err := h.content.CreateTutorial(r.Context(), p, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTutorialVO(*t))
}

func (h *handlers) updateTutorial(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req tutorialBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	t, err := h.content.UpdateTutorial(r.Context(), p, id, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTutorialVO(*t))
}

func (h *handlers) deleteTutorial(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.content.DeleteTutorial(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// ---- 共享账号 ----

// sharedAccountBody 对应 hub-api.yaml SharedAccountInput。
type sharedAccountBody struct {
	Name        string  `json:"name"`
	Category    string  `json:"category"`
	Account     string  `json:"account"`
	Credential  string  `json:"credential"`
	Remark      string  `json:"remark"`
	SortOrder   int     `json:"sortOrder"`
	IsPublished bool    `json:"isPublished"`
	PlanIDs     []int64 `json:"planIds"`
}

func (b sharedAccountBody) input() service.SharedAccountInput {
	return service.SharedAccountInput{Name: b.Name, Category: b.Category, Account: b.Account, Secret: b.Credential, Remark: b.Remark,
		SortOrder: b.SortOrder, IsPublished: b.IsPublished, PlanIDs: b.PlanIDs}
}

// sharedAccountVO 对应 hub-api.yaml SharedAccount；isPublished、planIds 只在管理端输出。
type sharedAccountVO struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Category    string  `json:"category"`
	Account     string  `json:"account"`
	Remark      string  `json:"remark"`
	SortOrder   int     `json:"sortOrder"`
	IsPublished *bool   `json:"isPublished,omitempty"`
	PlanIDs     []int64 `json:"planIds,omitempty"`
}

func toSharedAccountVO(a service.SharedAccount, forAdmin bool) sharedAccountVO {
	vo := sharedAccountVO{ID: a.ID, Name: a.Name, Category: a.Category, Account: a.Account, Remark: a.Remark, SortOrder: a.SortOrder}
	if forAdmin {
		published := a.IsPublished
		vo.IsPublished, vo.PlanIDs = &published, a.PlanIDs
		if vo.PlanIDs == nil {
			vo.PlanIDs = []int64{}
		}
	}
	return vo
}

type sharedCredentialVO struct {
	Credential string `json:"credential"`
}

func writeSharedAccounts(w http.ResponseWriter, list []service.SharedAccount, forAdmin bool) {
	out := make([]sharedAccountVO, 0, len(list))
	for _, a := range list {
		out = append(out, toSharedAccountVO(a, forAdmin))
	}
	writeOK(w, out)
}

func (h *handlers) listSharedAccounts(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	list, err := h.content.ListSharedAccounts(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeSharedAccounts(w, list, true)
}

func (h *handlers) createSharedAccount(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req sharedAccountBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.content.CreateSharedAccount(r.Context(), p, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toSharedAccountVO(*a, true))
}

func (h *handlers) updateSharedAccount(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req sharedAccountBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.content.UpdateSharedAccount(r.Context(), p, id, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toSharedAccountVO(*a, true))
}

func (h *handlers) deleteSharedAccount(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.content.DeleteSharedAccount(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

func (h *handlers) getSharedAccountCredential(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	secret, err := h.content.RevealSharedAccount(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, sharedCredentialVO{Credential: secret})
}

func (h *handlers) listUserSharedAccounts(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	list, err := h.content.ListVisibleSharedAccounts(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeSharedAccounts(w, list, false)
}

func (h *handlers) getUserSharedAccountCredential(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	secret, err := h.content.RevealVisibleSharedAccount(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, sharedCredentialVO{Credential: secret})
}
