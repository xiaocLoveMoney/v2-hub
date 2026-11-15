package api

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

// 余额流水与充值码接口（docs/03 §5.14、§5.15），与 hub-api.yaml 的 /api/admin/wallet-ledger、/api/admin/topup-codes、
// /api/admin/users/{id}/balance、/api/user/wallet/* 逐条对应。金额一律整数分。

const (
	queryBatch       = "batch"
	msgBadUserFilter = "userId 须为正整数"
)

// WalletManager 是余额流水与充值码的业务接口，由 service.WalletService 实现。
type WalletManager interface {
	ListLedger(ctx context.Context, f service.LedgerFilter, pq service.PageQuery) (*service.Page[service.LedgerEntry], error)
	ListOwnLedger(ctx context.Context, p *service.UserPrincipal, pq service.PageQuery) (*service.Page[service.LedgerEntry], error)
	AdjustBalance(ctx context.Context, actor *service.AdminPrincipal, userID, amount int64, remark string, ip *netip.Addr) error
	CreateTopupCodes(ctx context.Context, actor *service.AdminPrincipal, in service.TopupBatchInput, ip *netip.Addr) ([]string, error)
	ListTopupCodes(ctx context.Context, f service.TopupFilter, pq service.PageQuery) (*service.Page[service.TopupCode], error)
	SetTopupCodeDisabled(ctx context.Context, actor *service.AdminPrincipal, id int64, disabled bool, ip *netip.Addr) (*service.TopupCode, error)
	DeleteTopupCode(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	RevealTopupCode(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) (string, error)
	Redeem(ctx context.Context, p *service.UserPrincipal, code string, ip *netip.Addr) (*service.RedeemResult, error)
}

// optionalUserID 读取可选的 userId 查询参数；省略返回 0。
func optionalUserID(r *http.Request) (int64, error) {
	return optionalPositiveInt64(r, queryUserID, msgBadUserFilter)
}

// ledgerEntryVO 对应 hub-api.yaml LedgerEntry；用户侧不带 userId / username / adminUsername。
type ledgerEntryVO struct {
	ID            int64     `json:"id"`
	Kind          string    `json:"kind"`
	Amount        int64     `json:"amount"`
	BalanceAfter  int64     `json:"balanceAfter"`
	Remark        string    `json:"remark"`
	CreatedAt     time.Time `json:"createdAt"`
	UserID        *int64    `json:"userId,omitempty"`
	Username      *string   `json:"username,omitempty"`
	AdminUsername *string   `json:"adminUsername,omitempty"`
}

func toLedgerPageVO(page *service.Page[service.LedgerEntry], isAdmin bool) pageVO[ledgerEntryVO] {
	return toPageVO(page, func(e service.LedgerEntry) ledgerEntryVO {
		vo := ledgerEntryVO{ID: e.ID, Kind: e.Kind, Amount: e.Amount, BalanceAfter: e.BalanceAfter, Remark: e.Remark, CreatedAt: e.CreatedAt}
		if isAdmin {
			vo.Username, vo.AdminUsername = &e.Username, &e.AdminUsername
			if e.UserID != 0 {
				vo.UserID = &e.UserID
			}
		}
		return vo
	})
}

func (h *handlers) listWalletLedger(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	userID, err := optionalUserID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.wallet.ListLedger(r.Context(), service.LedgerFilter{UserID: userID, Kind: r.URL.Query().Get(queryKind)}, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toLedgerPageVO(page, true))
}

func (h *handlers) listOwnLedger(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.wallet.ListOwnLedger(r.Context(), p, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toLedgerPageVO(page, false))
}

// balanceAdjustBody 对应 hub-api.yaml BalanceAdjustInput。
type balanceAdjustBody struct {
	Amount int64  `json:"amount"`
	Remark string `json:"remark"`
}

func (h *handlers) adjustUserBalance(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req balanceAdjustBody
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.wallet.AdjustBalance(r.Context(), p, id, req.Amount, req.Remark, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	u, err := h.users.GetUser(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toUserVO(*u))
}

// topupBatchBody 对应 hub-api.yaml TopupCodeBatchInput。
type topupBatchBody struct {
	Amount    int64      `json:"amount"`
	Count     int        `json:"count"`
	ExpiresAt *time.Time `json:"expiresAt"`
	Batch     string     `json:"batch"`
}

// topupBatchVO 对应 hub-api.yaml TopupCodeBatch：明文只在生成时返回这一次（Ruling-31）。
type topupBatchVO struct {
	Amount int64    `json:"amount"`
	Codes  []string `json:"codes"`
}

// topupCodeVO 对应 hub-api.yaml TopupCode。
type topupCodeVO struct {
	ID             int64      `json:"id"`
	CodeTail       string     `json:"codeTail"`
	Amount         int64      `json:"amount"`
	Batch          string     `json:"batch"`
	Status         string     `json:"status"`
	ExpiresAt      *time.Time `json:"expiresAt"`
	IsDisabled     bool       `json:"isDisabled"`
	UsedByUserID   *int64     `json:"usedByUserId"`
	UsedByUsername string     `json:"usedByUsername"`
	UsedAt         *time.Time `json:"usedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
}

func toTopupCodeVO(c service.TopupCode) topupCodeVO {
	vo := topupCodeVO{ID: c.ID, CodeTail: c.CodeTail, Amount: c.Amount, Batch: c.Batch, Status: c.Status, ExpiresAt: c.ExpiresAt,
		IsDisabled: c.Disabled, UsedByUsername: c.UsedByUsername, UsedAt: c.UsedAt, CreatedAt: c.CreatedAt}
	if c.UsedByUserID != 0 {
		vo.UsedByUserID = &c.UsedByUserID
	}
	return vo
}

func (h *handlers) listTopupCodes(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	query := r.URL.Query()
	page, err := h.wallet.ListTopupCodes(r.Context(), service.TopupFilter{Status: query.Get(queryStatus), Batch: query.Get(queryBatch)}, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toPageVO(page, toTopupCodeVO))
}

func (h *handlers) createTopupCodes(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req topupBatchBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in := service.TopupBatchInput{Amount: req.Amount, Count: req.Count, ExpiresAt: req.ExpiresAt, Batch: req.Batch}
	codes, err := h.wallet.CreateTopupCodes(r.Context(), p, in, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, topupBatchVO{Amount: req.Amount, Codes: codes})
}

// topupUpdateBody 对应 hub-api.yaml TopupCodeUpdateInput。
type topupUpdateBody struct {
	IsDisabled bool `json:"isDisabled"`
}

func (h *handlers) updateTopupCode(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req topupUpdateBody
	id, err := decodeWithID(w, r, &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	c, err := h.wallet.SetTopupCodeDisabled(r.Context(), p, id, req.IsDisabled, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTopupCodeVO(*c))
}

func (h *handlers) deleteTopupCode(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.wallet.DeleteTopupCode(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// topupSecretVO 对应 hub-api.yaml TopupCodeSecret。
type topupSecretVO struct {
	Code string `json:"code"`
}

func (h *handlers) revealTopupCode(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	code, err := h.wallet.RevealTopupCode(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, topupSecretVO{Code: code})
}

// redeemBody 对应 hub-api.yaml RedeemInput。
type redeemBody struct {
	Code string `json:"code"`
}

// redeemVO 对应 hub-api.yaml RedeemResult。
type redeemVO struct {
	Amount  int64 `json:"amount"`
	Balance int64 `json:"balance"`
}

func (h *handlers) redeemTopupCode(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req redeemBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.wallet.Redeem(r.Context(), p, req.Code, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, redeemVO{Amount: res.Amount, Balance: res.Balance})
}
