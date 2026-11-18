package service

import (
	"context"
	"fmt"
	"net/netip"

	"v2-hub-server/internal/dao/sqlcgen"
)

const (
	// ClientFlowVision 是 VLESS 的 Vision 流控（Ruling-77）；空串为不带。
	ClientFlowVision = "xtls-rprx-vision"

	// ActionServerClientFlow 是开关服务器 VLESS 流控的审计动作。
	ActionServerClientFlow = "server.client_flow"

	msgBadClientFlow = "流控只能是空或 xtls-rprx-vision"
)

// isValidClientFlow 报告 flow 是不是服务器允许的流控取值。
func isValidClientFlow(flow string) bool {
	return flow == "" || flow == ClientFlowVision
}

// SetServerClientFlow 开关服务器的 VLESS 流控（docs/03 §5.1 “VLESS 流控”，Ruling-77）并触发下发对账，
// 由对账给这台面板上的 client 带上或去掉 flow。
func (s *UserService) SetServerClientFlow(ctx context.Context, actor *AdminPrincipal, serverID int64, flow string, ip *netip.Addr) (*Server, error) {
	if !isValidClientFlow(flow) {
		return nil, newError(CodeInvalidArgument, msgBadClientFlow)
	}
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.SetServerClientFlow(ctx, sqlcgen.SetServerClientFlowParams{ID: serverID, ClientFlow: flow})
		if err != nil {
			return fmt.Errorf("set client flow of server %d: %w", serverID, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgServerNotFound)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionServerClientFlow, TargetType: targetServer,
			TargetID: fmt.Sprint(serverID), ClientIP: ip, Detail: map[string]any{"clientFlow": flow},
		})
	})
	if err != nil {
		return nil, err
	}
	s.TriggerReconcile()
	return s.panels.GetServer(ctx, serverID)
}
