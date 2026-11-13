package api

import (
	"net/http"
	"time"

	"v2-hub-server/internal/service"
)

// 邀请码管理接口（docs/03 §5.8 邀请码；权限归 user:*），与 hub-api.yaml 的 /api/admin/invite-codes 逐条对应。

const queryKind = "kind"

// inviteCodeBody 对应 hub-api.yaml InviteCodeInput。
type inviteCodeBody struct {
	MaxUses    int        `json:"maxUses"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	Remark     string     `json:"remark"`
	IsDisabled bool       `json:"isDisabled"`
}

func (b inviteCodeBody) input() service.InviteCodeInput {
	return service.InviteCodeInput{MaxUses: b.MaxUses, ExpiresAt: b.ExpiresAt, Remark: b.Remark, IsDisabled: b.IsDisabled}
}

// inviteCodeVO 对应 hub-api.yaml InviteCode。
type inviteCodeVO struct {
	ID            int64      `json:"id"`
	Code          string     `json:"code"`
	Kind          string     `json:"kind"`
	OwnerUserID   *int64     `json:"ownerUserId"`
	OwnerUsername string     `json:"ownerUsername"`
	MaxUses       int        `json:"maxUses"`
	UsedCount     int        `json:"usedCount"`
	ExpiresAt     *time.Time `json:"expiresAt"`
	IsDisabled    bool       `json:"isDisabled"`
	Remark        string     `json:"remark"`
	CreatedAt     time.Time  `json:"createdAt"`
}

func toInviteCodeVO(c service.InviteCode) inviteCodeVO {
	return inviteCodeVO{ID: c.ID, Code: c.Code, Kind: c.Kind, OwnerUserID: c.OwnerUserID, OwnerUsername: c.OwnerUsername, MaxUses: c.MaxUses,
		UsedCount: c.UsedCount, ExpiresAt: c.ExpiresAt, IsDisabled: c.IsDisabled, Remark: c.Remark, CreatedAt: c.CreatedAt}
}

type inviteCodePageVO struct {
	Items []inviteCodeVO `json:"items"`
	Total int64          `json:"total"`
}

func (h *handlers) listInviteCodes(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.account.ListInviteCodes(r.Context(), r.URL.Query().Get(queryKind), pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := inviteCodePageVO{Items: make([]inviteCodeVO, 0, len(page.Items)), Total: page.Total}
	for _, c := range page.Items {
		out.Items = append(out.Items, toInviteCodeVO(c))
	}
	writeOK(w, out)
}

func (h *handlers) createInviteCode(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req inviteCodeBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	c, err := h.account.CreateInviteCode(r.Context(), p, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toInviteCodeVO(*c))
}

func (h *handlers) updateInviteCode(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req inviteCodeBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	c, err := h.account.UpdateInviteCode(r.Context(), p, id, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toInviteCodeVO(*c))
}

func (h *handlers) deleteInviteCode(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.account.DeleteInviteCode(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}
