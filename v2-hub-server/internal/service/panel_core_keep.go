package service

import (
	"context"
	"fmt"
	"log/slog"

	"v2-hub-server/internal/manager/panel"
)

// ActionServerKeepCore 是平台关掉面板“client 停用即重启内核”设置的审计动作（Ruling-94）。
const ActionServerKeepCore = "server.keep_core_on_client_drop"

// keepCoreOnClientDrop 让面板在 client 被停用 / 删除时不重启内核（docs/03 §5.2 对账，Ruling-94）：
// 面板有这项能力且设置还开着时关掉并记审计；被人改回去，下一轮同步再关。
// 失败只记日志、不影响本次同步，下一轮同步再试。
func (s *PanelService) keepCoreOnClientDrop(ctx context.Context, serverID int64, p panel.Provider) {
	keeper, ok := p.(panel.CoreKeeper)
	if !ok {
		return
	}
	isChanged, err := keeper.KeepCoreOnClientDrop(ctx)
	if err != nil {
		slog.Warn("keep core on client drop", "server_id", serverID, "err", err)
		return
	}
	if !isChanged {
		return
	}
	entry := AuditEntry{ActorType: ActorSystem, Action: ActionServerKeepCore, TargetType: targetServer, TargetID: fmt.Sprint(serverID)}
	if err := writeAudit(ctx, s.q, entry); err != nil {
		slog.Error("audit keep core on client drop", "server_id", serverID, "err", err)
	}
}
