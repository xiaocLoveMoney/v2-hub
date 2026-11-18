package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/remote"
)

const (
	protoNameTCP = "TCP"
	protoNameUDP = "UDP"

	// msgHostPortInUse：%[1]d 端口，%[2]s 协议。
	msgHostPortInUse = "端口 %[1]d/%[2]s 已被主机上的其它程序占用（也可能是面板上的其它入站）；换一个端口，否则面板的 Xray 会因绑不上端口整个起不来"
)

// CreateInbound 先查主机端口（服务器绑了 SSH 时），再交给 PanelService 新建入站（docs/03 §5.1 “主机端口检查”，Ruling-78）。
func (s *HostService) CreateInbound(ctx context.Context, actor *AdminPrincipal, serverID int64, in InboundCreateInput, ip *netip.Addr) (*Inbound, error) {
	if in.Enabled {
		listen, err := s.panels.NewInboundListen(in)
		if err != nil {
			return nil, err
		}
		if err := s.checkHostPort(ctx, serverID, listen); err != nil {
			return nil, err
		}
	}
	return s.panels.CreateInbound(ctx, actor, serverID, in, ip)
}

// UpdateInbound 改了启用中入站的端口时先查主机端口，再交给 PanelService；端口没变的不查（端口由这个入站自己占着）。
func (s *HostService) UpdateInbound(ctx context.Context, actor *AdminPrincipal, id int64, in InboundUpdateInput, ip *netip.Addr) (*Inbound, error) {
	cur, err := s.panels.GetInbound(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur.IsEnabled && in.Port != cur.Port {
		config := cur.Config
		if len(in.Config) > 0 {
			config = in.Config
		}
		tcp, udp := listenTransports(cur.Protocol, config)
		if err := s.checkHostPort(ctx, cur.ServerID, InboundListen{Port: in.Port, TCP: tcp, UDP: udp}); err != nil {
			return nil, err
		}
	}
	return s.panels.UpdateInbound(ctx, actor, id, in, ip)
}

// SetInboundEnabled 启用停用中的入站时先查主机端口（停用期间端口可能被别的程序占了），再交给 PanelService。
func (s *HostService) SetInboundEnabled(ctx context.Context, actor *AdminPrincipal, id int64, enabled bool, ip *netip.Addr) (*Inbound, error) {
	if enabled {
		cur, err := s.panels.GetInbound(ctx, id)
		if err != nil {
			return nil, err
		}
		if !cur.IsEnabled {
			tcp, udp := listenTransports(cur.Protocol, cur.Config)
			if err := s.checkHostPort(ctx, cur.ServerID, InboundListen{Port: cur.Port, TCP: tcp, UDP: udp}); err != nil {
				return nil, err
			}
		}
	}
	return s.panels.SetInboundEnabled(ctx, actor, id, enabled, ip)
}

// checkHostPort 经 SSH 读主机正在监听的端口，与 listen 冲突就拒绝（400）。
// 服务器没绑 SSH 时不查；读不到（SSH 连不上、主机密钥变了等）时不拦，只记日志：这是防呆，不该挡住入站管理。
func (s *HostService) checkHostPort(ctx context.Context, serverID int64, listen InboundListen) error {
	hostID, err := s.q.GetServerHostIDByServer(ctx, pgtype.Int8{Int64: serverID, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("host of server %d: %w", serverID, err)
	}
	host, err := s.hostRow(ctx, hostID)
	if err != nil {
		return err
	}
	deployer, ok := s.deployers.Get(host.ProviderType)
	if !ok {
		return nil
	}
	var listening []deploy.Port
	target := endpoint{address: host.Address, port: int(host.SshPort), credentialID: host.CredentialID, pinnedKey: host.HostKeySha256}
	err = s.withRunner(ctx, target, func(ctx context.Context, r remote.Runner) error {
		var err error
		listening, err = deployer.ListeningPorts(ctx, r)
		return err
	})
	if err != nil {
		slog.Warn("read host listening ports before inbound write", "server_id", serverID, "host_id", host.ID, "err", err)
		return nil
	}
	for _, p := range listening {
		if p.Number != listen.Port {
			continue
		}
		if p.UDP && listen.UDP {
			return newError(CodeInvalidArgument, fmt.Sprintf(msgHostPortInUse, listen.Port, protoNameUDP))
		}
		if !p.UDP && listen.TCP {
			return newError(CodeInvalidArgument, fmt.Sprintf(msgHostPortInUse, listen.Port, protoNameTCP))
		}
	}
	return nil
}
