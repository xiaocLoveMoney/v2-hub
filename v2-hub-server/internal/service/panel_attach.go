package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"v2-hub-server/internal/dao/sqlcgen"
)

// auditSourceHostTask 标在审计详情里，说明这次改动来自 SSH 部署或运维任务（docs/03 §5.22）。
const auditSourceHostTask = "host_task"

// AttachPanel 把经 SSH 得到的面板接入信息落成一台服务器（docs/03 §5.22 部署流程 ④），返回服务器 id。
// 平台里已有一台面板标识相同、且面板地址的主机名也相同的服务器时，只更新那台的接入信息与令牌，不重复新建；
// 否则按 §5.1 新建并首次同步。只认标识不认地址会把“从同一份镜像克隆出来的两台机器”错连成一台。
// adminID 是发起任务的管理员，记在审计里；管理员已被删除时为 0。
func (s *PanelService) AttachPanel(ctx context.Context, adminID int64, in ServerInput) (int64, error) {
	info, err := s.TestServer(ctx, in.ServerConnInput)
	if err != nil {
		return 0, err
	}
	existing, err := s.findSamePanel(ctx, info.GUID, in.BaseURL)
	if err != nil {
		return 0, err
	}
	if existing != 0 {
		if err := s.ReplaceServerConn(ctx, adminID, existing, in.ServerConnInput); err != nil {
			return 0, err
		}
		return existing, nil
	}
	srv, err := s.CreateServer(ctx, &AdminPrincipal{AdminID: adminID}, in, nil)
	if err != nil {
		return 0, err
	}
	return srv.ID, nil
}

// findSamePanel 找面板标识与主机名都和这次相同的服务器，没有返回 0。
func (s *PanelService) findSamePanel(ctx context.Context, guid, baseURL string) (int64, error) {
	if guid == "" {
		return 0, nil
	}
	rows, err := s.q.ListServersByPanelGUID(ctx, guid)
	if err != nil {
		return 0, fmt.Errorf("find servers by panel guid: %w", err)
	}
	want, err := url.Parse(baseURL)
	if err != nil {
		return 0, nil // 地址已由 TestServer 校验过，走不到这里；解析不了就按没有处理
	}
	for _, row := range rows {
		if got, err := url.Parse(row.BaseUrl); err == nil && strings.EqualFold(got.Hostname(), want.Hostname()) {
			return row.ID, nil
		}
	}
	return 0, nil
}

// ReplaceServerConn 改写一台服务器的接入信息（重签令牌、配置证书、接管已知面板）；conn.APIToken 为空表示沿用原令牌。
// 先用新信息探测，通过才保存；探测不通返回 502，服务器的原信息不变。
func (s *PanelService) ReplaceServerConn(ctx context.Context, adminID, serverID int64, conn ServerConnInput) error {
	conn, err := s.validateConn(conn, false)
	if err != nil {
		return err
	}
	tokenChanged := conn.APIToken != ""
	if !tokenChanged {
		current, err := s.q.GetServerConn(ctx, serverID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newError(CodeNotFound, msgServerNotFound)
		}
		if err != nil {
			return fmt.Errorf("get server %d: %w", serverID, err)
		}
		token, err := s.sealer.Open(current.ApiTokenEnc, tokenAAD(serverID))
		if err != nil {
			return fmt.Errorf("open token of server %d: %w", serverID, err)
		}
		conn.APIToken = string(token)
	}
	info, err := s.TestServer(ctx, conn)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.UpdateServerConn(ctx, sqlcgen.UpdateServerConnParams{
			ID: serverID, BaseUrl: conn.BaseURL, TlsMode: conn.TLSMode, CertPinSha256: textOrNull(conn.CertPinSHA256),
		})
		if err != nil {
			return fmt.Errorf("update conn of server %d: %w", serverID, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgServerNotFound)
		}
		if tokenChanged {
			sealed, err := s.sealer.Seal([]byte(conn.APIToken), tokenAAD(serverID))
			if err != nil {
				return fmt.Errorf("seal server token: %w", err)
			}
			if err := q.SetServerToken(ctx, sqlcgen.SetServerTokenParams{ID: serverID, ApiTokenEnc: sealed}); err != nil {
				return fmt.Errorf("save server token: %w", err)
			}
		}
		if err := q.SetServerProbeResult(ctx, probeParams(serverID, info, nil, s.now())); err != nil {
			return fmt.Errorf("save probe result: %w", err)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: adminID, Action: ActionServerUpdate, TargetType: targetServer, TargetID: fmt.Sprint(serverID),
			Detail: map[string]any{"source": auditSourceHostTask, "tokenChanged": tokenChanged, "baseUrl": redactURL(conn.BaseURL), "tlsMode": conn.TLSMode},
		})
	})
}
