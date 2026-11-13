package api

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

// 服务端链式（docs/03 §5.19）：用户侧规则接口与管理端下发状态 / 重试 / 全站规则接口。

// ChainApplyManager 是管理端服务端链式接口需要的能力（由 service.ChainApplier 实现）。
type ChainApplyManager interface {
	Status(ctx context.Context, serverID int64) (*service.ChainApply, error)
	Retry(ctx context.Context, actor *service.AdminPrincipal, serverID int64, ip *netip.Addr) (*service.ChainApply, error)
	ListServerRules(ctx context.Context, f service.ServerChainRuleFilter, pq service.PageQuery) (*service.Page[service.AdminServerChainRule], error)
}

type serverChainExitVO struct {
	NodeID   int64  `json:"nodeId"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
}

// serverChainRuleVO 对应 hub-api.yaml ServerChainRule。
type serverChainRuleVO struct {
	ID            int64             `json:"id"`
	Entry         chainEntryVO      `json:"entry"`
	Exit          serverChainExitVO `json:"exit"`
	IsEnabled     bool              `json:"isEnabled"`
	Status        string            `json:"status"`
	BlockedReason string            `json:"blockedReason"`
	ApplyStatus   string            `json:"applyStatus"`
	CreatedAt     time.Time         `json:"createdAt"`
}

func toServerChainRuleVO(r service.ServerChainRule) serverChainRuleVO {
	return serverChainRuleVO{
		ID: r.ID, IsEnabled: r.Enabled, Status: r.Status, BlockedReason: r.BlockedReason, ApplyStatus: r.ApplyStatus, CreatedAt: r.CreatedAt,
		Entry: chainEntryVO{NodeID: r.EntryNodeID, Name: r.EntryName},
		Exit:  serverChainExitVO{NodeID: r.ExitNodeID, Name: r.ExitName, Protocol: r.ExitProtocol},
	}
}

// serverChainRuleCreateRequest 对应 hub-api.yaml ServerChainRuleInput；isEnabled 省略按启用。
type serverChainRuleCreateRequest struct {
	EntryNodeID int64 `json:"entryNodeId"`
	ExitNodeID  int64 `json:"exitNodeId"`
	IsEnabled   *bool `json:"isEnabled"`
}

// serverChainRuleUpdateRequest 对应 hub-api.yaml ServerChainRuleUpdateInput。
type serverChainRuleUpdateRequest struct {
	IsEnabled bool `json:"isEnabled"`
}

func (h *handlers) createServerChainRule(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req serverChainRuleCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in := service.ServerChainRuleInput{EntryNodeID: req.EntryNodeID, ExitNodeID: req.ExitNodeID, Enabled: true}
	if req.IsEnabled != nil {
		in.Enabled = *req.IsEnabled
	}
	rule, err := h.chain.CreateServerRule(r.Context(), p, in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toServerChainRuleVO(*rule))
}

func (h *handlers) updateServerChainRule(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req serverChainRuleUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	rule, err := h.chain.UpdateServerRule(r.Context(), p, id, req.IsEnabled)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toServerChainRuleVO(*rule))
}

func (h *handlers) deleteServerChainRule(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.chain.DeleteServerRule(r.Context(), p, id); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// chainApplyVO 对应 hub-api.yaml ChainApply。
type chainApplyVO struct {
	ServerID         int64      `json:"serverId"`
	IsSupported      bool       `json:"isSupported"`
	Status           string     `json:"status"`
	RuleCount        int        `json:"ruleCount"`
	AppliedRuleCount int        `json:"appliedRuleCount"`
	FailCount        int        `json:"failCount"`
	IsRetryStopped   bool       `json:"isRetryStopped"`
	LastError        string     `json:"lastError"`
	LastAttemptAt    *time.Time `json:"lastAttemptAt"`
	NextAttemptAt    *time.Time `json:"nextAttemptAt"`
	AppliedAt        *time.Time `json:"appliedAt"`
}

func toChainApplyVO(a *service.ChainApply) chainApplyVO {
	return chainApplyVO{
		ServerID: a.ServerID, IsSupported: a.Supported, Status: a.Status, RuleCount: a.RuleCount, AppliedRuleCount: a.AppliedRuleCount,
		FailCount: a.FailCount, IsRetryStopped: a.RetryStopped, LastError: a.LastError,
		LastAttemptAt: a.LastAttemptAt, NextAttemptAt: a.NextAttemptAt, AppliedAt: a.AppliedAt,
	}
}

func (h *handlers) getServerChainApply(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.chainApply.Status(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toChainApplyVO(a))
}

func (h *handlers) retryServerChainApply(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := h.chainApply.Retry(r.Context(), p, id, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toChainApplyVO(a))
}

type adminChainUserVO struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type adminChainEntryVO struct {
	NodeID     int64  `json:"nodeId"`
	Name       string `json:"name"`
	ServerID   int64  `json:"serverId"`
	ServerName string `json:"serverName"`
}

type adminChainExitVO struct {
	NodeID int64  `json:"nodeId"`
	Name   string `json:"name"`
}

// adminServerChainRuleVO 对应 hub-api.yaml AdminServerChainRule。
type adminServerChainRuleVO struct {
	ID            int64             `json:"id"`
	User          adminChainUserVO  `json:"user"`
	Entry         adminChainEntryVO `json:"entry"`
	Exit          adminChainExitVO  `json:"exit"`
	IsEnabled     bool              `json:"isEnabled"`
	Status        string            `json:"status"`
	BlockedReason string            `json:"blockedReason"`
	ApplyStatus   string            `json:"applyStatus"`
	CreatedAt     time.Time         `json:"createdAt"`
}

func toAdminServerChainRuleVO(r service.AdminServerChainRule) adminServerChainRuleVO {
	return adminServerChainRuleVO{
		ID: r.ID, IsEnabled: r.Enabled, Status: r.Status, BlockedReason: r.BlockedReason, ApplyStatus: r.ApplyStatus, CreatedAt: r.CreatedAt,
		User:  adminChainUserVO{ID: r.UserID, Username: r.Username},
		Entry: adminChainEntryVO{NodeID: r.EntryNodeID, Name: r.EntryName, ServerID: r.EntryServerID, ServerName: r.EntryServerName},
		Exit:  adminChainExitVO{NodeID: r.ExitNodeID, Name: r.ExitName},
	}
}

func (h *handlers) listServerChainRules(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
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
	serverID, err := optionalQueryID(r, queryServerID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.chainApply.ListServerRules(r.Context(), service.ServerChainRuleFilter{UserID: userID, ServerID: serverID}, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, pageVO[adminServerChainRuleVO]{Items: mapSlice(page.Items, toAdminServerChainRuleVO), Total: page.Total})
}
