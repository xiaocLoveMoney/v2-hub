package service

import (
	"context"
	"errors"

	"v2-hub-server/internal/manager/remote"
)

var (
	// errProbeNoHost 表示服务器没绑 SSH。
	errProbeNoHost = errors.New("server has no ssh host")
	// errProbeHostUnverified 表示主机密钥还没固定（没做过部署或校验连接）。
	errProbeHostUnverified = errors.New("host key not pinned yet")
)

// withServerHost 给只读探测用（docs/03 §5.23 IP 质量检测）：连上服务器绑定的主机并执行 fn。
// 没绑 SSH 返回 errProbeNoHost；主机密钥还没固定返回 errProbeHostUnverified——只读探测不做首次固定，交给部署 / 校验连接任务。
// 不套 withRunner 的时限与 502 转换：调用方自己设时限，并把失败归类后写进报告。
func (s *HostService) withServerHost(ctx context.Context, serverID int64, fn func(ctx context.Context, r remote.Runner) error) error {
	srv, err := s.panels.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	if srv.HostID == nil {
		return errProbeNoHost
	}
	host, err := s.hostRow(ctx, *srv.HostID)
	if err != nil {
		return err
	}
	if host.HostKeySha256 == "" {
		return errProbeHostUnverified
	}
	runner, err := s.dial(ctx, host.Address, int(host.SshPort), host.CredentialID, host.HostKeySha256)
	if err != nil {
		return err
	}
	defer closeRunner(runner)
	return fn(ctx, runner)
}
