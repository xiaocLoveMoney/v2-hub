package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 密码凭据的确认（docs/03 §5.22，Ruling-97）：平台对还没固定主机密钥的地址接受任何主机密钥，随后把密码发给对端，
// 所以 password 类型的凭据用到一个还没用过它的目标上时，操作者须再输入一次该凭据的密码。
// 这只是知情证明，不改已存的凭据；私钥类型的凭据不受影响（私钥不会发给对端）。

const (
	msgCredentialSecretRequired = "这份密码凭据还没用在这个地址上，请再输入一次该凭据的密码"
	msgCredentialSecretMismatch = "凭据密码不正确"
	msgCredentialSecretTooMany  = "凭据密码输错次数过多，请稍后再试"
)

// hostTarget 是一次连接的（地址，端口）；地址须已规范化，与 server_host 里存的形式一致。
type hostTarget struct {
	address string
	port    int
}

// confirmCredentialForNewTargets 在凭据是密码类型、且 targets 里有（凭据，地址，端口）还没出现在 server_host 时要求确认，
// 整批只比对一次。返回是否做过确认。
// 错误：CodeNotFound（凭据不存在）、CodeInvalidArgument（缺密码或不一致）、CodeRateLimited。
func (s *HostService) confirmCredentialForNewTargets(ctx context.Context, actor *AdminPrincipal, credentialID int64, targets []hostTarget, secret string) (bool, error) {
	row, err := s.credentialAuthRow(ctx, credentialID)
	if err != nil {
		return false, err
	}
	if row.AuthType != SSHAuthPassword {
		return false, nil
	}
	isNew, err := s.hasUnusedTarget(ctx, credentialID, targets)
	if err != nil {
		return false, err
	}
	if !isNew {
		return false, nil
	}
	return true, s.compareCredentialSecret(actor, row, secret)
}

// confirmCredential 在凭据是密码类型时无条件要求确认（修改主机的目标变了、重置主机密钥），返回是否做过确认。
// 错误同 confirmCredentialForNewTargets。
func (s *HostService) confirmCredential(ctx context.Context, actor *AdminPrincipal, credentialID int64, secret string) (bool, error) {
	row, err := s.credentialAuthRow(ctx, credentialID)
	if err != nil {
		return false, err
	}
	if row.AuthType != SSHAuthPassword {
		return false, nil
	}
	return true, s.compareCredentialSecret(actor, row, secret)
}

func (s *HostService) credentialAuthRow(ctx context.Context, credentialID int64) (sqlcgen.GetSSHCredentialSecretRow, error) {
	row, err := s.q.GetSSHCredentialSecret(ctx, credentialID)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, newError(CodeNotFound, msgCredentialNotFound)
	}
	if err != nil {
		return row, fmt.Errorf("get ssh credential %d: %w", credentialID, err)
	}
	return row, nil
}

// hasUnusedTarget 报告 targets 里是否有这份凭据还没用过的（地址，端口）。
func (s *HostService) hasUnusedTarget(ctx context.Context, credentialID int64, targets []hostTarget) (bool, error) {
	for _, t := range targets {
		n, err := s.q.CountServerHostsByTarget(ctx, sqlcgen.CountServerHostsByTargetParams{
			CredentialID: credentialID, Address: t.address, SshPort: int32(t.port), //nolint:gosec // 端口已校验在 1–65535
		})
		if err != nil {
			return false, fmt.Errorf("count hosts of credential %d: %w", credentialID, err)
		}
		if n == 0 {
			return true, nil
		}
	}
	return false, nil
}

// compareCredentialSecret 解密已存密码并与 secret 做常量时间比较；不一致按管理员计入频控。密码不进日志与错误信息。
func (s *HostService) compareCredentialSecret(actor *AdminPrincipal, row sqlcgen.GetSSHCredentialSecretRow, secret string) error {
	key := strconv.FormatInt(actor.AdminID, 10)
	if s.confirms.IsLocked(key) {
		return newError(CodeRateLimited, msgCredentialSecretTooMany)
	}
	if secret == "" {
		return newError(CodeInvalidArgument, msgCredentialSecretRequired)
	}
	stored, err := s.sealer.Open(row.SecretEnc, secretAAD(row.ID))
	if err != nil {
		return fmt.Errorf("open ssh secret %d: %w", row.ID, err)
	}
	if subtle.ConstantTimeCompare([]byte(secret), stored) != 1 {
		s.confirms.RecordFailure(key)
		return newError(CodeInvalidArgument, msgCredentialSecretMismatch)
	}
	s.confirms.Reset(key)
	return nil
}
