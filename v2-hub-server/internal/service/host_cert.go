package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/acmecert"
	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/dns"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/remote"
)

const (
	hostStepDNS         = "dns"
	hostStepIssue       = "issue"
	hostStepInstallCert = "install_cert"

	// certRenewBatch 是一轮自动续期最多排的任务数；剩下的等下一轮，避免一次把签发方的额度用光。
	certRenewBatch = int32(20)
	// certFallbackTimeout 是“按域名连不上、退回指纹接入”这一步自己的时限：走到这里时任务的时限可能已经用完，
	// 而证书已经换了，不退回的话服务器会一直对不上原来固定的指纹。
	certFallbackTimeout = time.Minute

	msgCertDomainDown  = "证书已装好，但平台按域名连不上面板（解析可能还没传到平台所在的网络），暂按证书指纹接入；稍后重做“配置证书”"
	msgCertAfterAttach = "面板已接入，但公开证书没有申请成功；排除问题后在 SSH 运维里重做“配置证书”，不需要重新部署"
)

// CertIssuer 向证书签发方申请域名证书（由 acmecert.Issuer 实现）。
type CertIssuer interface {
	// DirectoryURL 是签发方的目录地址，ACME 账号密钥按它区分。
	DirectoryURL() string
	// Issue 用 accountKey 对应的账号为 domain 申请证书，验证记录经 solver 写入。
	Issue(ctx context.Context, accountKey []byte, domain string, solver acmecert.Solver) (*acmecert.Certificate, error)
}

func acmeKeyAAD(directoryURL string) []byte {
	return fmt.Appendf(nil, "acme_account:%s:key", directoryURL)
}

// acmeAccountKey 取平台在当前签发方的 ACME 账号密钥，没有就生成并保存（docs/03 §5.22 “公开证书”）。
func (s *HostService) acmeAccountKey(ctx context.Context) ([]byte, error) {
	directoryURL := s.issuer.DirectoryURL()
	sealed, err := s.q.GetACMEAccountKey(ctx, directoryURL)
	if errors.Is(err, pgx.ErrNoRows) {
		sealed, err = s.createACMEAccount(ctx, directoryURL)
	}
	if err != nil {
		return nil, fmt.Errorf("get acme account key: %w", err)
	}
	key, err := s.sealer.Open(sealed, acmeKeyAAD(directoryURL))
	if err != nil {
		return nil, fmt.Errorf("open acme account key: %w", err)
	}
	return key, nil
}

// createACMEAccount 生成一把账号密钥并保存，返回库里最终留下的那一把的密文：
// 另一条任务可能抢先写入了它的，所以写完回读而不是直接用刚生成的。
func (s *HostService) createACMEAccount(ctx context.Context, directoryURL string) ([]byte, error) {
	key, err := acmecert.NewAccountKey()
	if err != nil {
		return nil, err
	}
	sealed, err := s.sealer.Seal(key, acmeKeyAAD(directoryURL))
	if err != nil {
		return nil, fmt.Errorf("seal acme account key: %w", err)
	}
	if err := s.q.CreateACMEAccount(ctx, sqlcgen.CreateACMEAccountParams{DirectoryUrl: directoryURL, KeyEnc: sealed}); err != nil {
		return nil, fmt.Errorf("save acme account key: %w", err)
	}
	return s.q.GetACMEAccountKey(ctx, directoryURL)
}

// txtSolver 把 ACME 的验证值经 DNS 服务商写成 TXT 记录。
type txtSolver struct {
	provider dns.Provider
}

func (s txtSolver) Present(ctx context.Context, name, value string) (func(context.Context) error, error) {
	remove, err := s.provider.SetTXT(ctx, name, value)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		err := remove(ctx)
		if err != nil {
			// 留下一条过期的验证记录不影响证书，记下来方便手动清理
			slog.Warn("remove acme txt record", "name", name, "err", err)
		}
		return err
	}, nil
}

// certError 把 DNS 服务商与签发方的错误归到任务的原因大类；其它错误原样返回。
func certError(err error) error {
	if de := dns.AsError(err); de != nil {
		return deploy.NewError(deploy.ReasonDNSFailed, de.Message)
	}
	if ae := acmecert.AsError(err); ae != nil {
		reason := deploy.ReasonCertFailed
		if ae.Kind == acmecert.KindDNS {
			reason = deploy.ReasonDNSFailed
		}
		return deploy.NewError(reason, ae.Message)
	}
	return err
}

func leafPin(leaf []byte) string {
	sum := sha256.Sum256(leaf)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// issueCert 做“公开证书”的 ①②：把域名解析到主机，再向签发方申请证书。这一段可能要几分钟，不占对账锁。
func (t *taskRun) issueCert(ctx context.Context) (*acmecert.Certificate, error) {
	host := t.host
	account, err := t.svc.openDNSAccount(ctx, host.DnsAccountID.Int64)
	if err != nil {
		return nil, err
	}
	t.secrets = append(t.secrets, account.token)
	provider, err := t.svc.dns.Open(account.providerType, dns.Account{Zone: account.zone, Token: account.token})
	if err != nil {
		return nil, fmt.Errorf("open dns provider %s: %w", account.providerType, err)
	}
	if host.Domain == host.Address {
		t.step(ctx, hostStepDNS, fmt.Sprintf("域名 %s 就是主机地址，不改解析", host.Domain))
	} else {
		t.step(ctx, hostStepDNS, fmt.Sprintf("把 %s 解析到 %s", host.Domain, host.Address))
		if err := provider.PointTo(ctx, host.Domain, host.Address); err != nil {
			return nil, err
		}
	}
	t.step(ctx, hostStepIssue, "向签发方申请证书（DNS 验证，通常要一两分钟）")
	key, err := t.svc.acmeAccountKey(ctx)
	if err != nil {
		return nil, err
	}
	cert, err := t.svc.issuer.Issue(ctx, key, host.Domain, txtSolver{provider: provider})
	if err != nil {
		return nil, err
	}
	t.logf("证书已签出，到期时间 %s", cert.NotAfter.UTC().Format(time.DateOnly))
	return cert, nil
}

// domainBaseURL 把面板根地址的协议换成 https，给出“主机名换成域名”与“主机名不变”两个版本，端口与路径都不变。
func domainBaseURL(baseURL, domain string) (string, string, error) {
	u, err := panel.ParseBaseURL(baseURL)
	if err != nil {
		return "", "", fmt.Errorf("parse base url: %w", err)
	}
	u.Scheme = httpsScheme
	byAddress := u.String()
	port := u.Port()
	u.Host = domain
	if port != "" {
		u.Host = net.JoinHostPort(domain, port)
	}
	return u.String(), byAddress, nil
}

// installCert 做“公开证书”的 ③④⑤：把证书装到面板上，把服务器改成按域名接入，记下到期时间。
// 调用方须已占用该服务器的对账锁：这一段会重启面板并改接入信息。
func (t *taskRun) installCert(ctx context.Context, d deploy.Deployer, r remote.Runner, serverID int64, cert *acmecert.Certificate) error {
	t.step(ctx, hostStepInstallCert, "把证书装到面板并重启")
	srv, err := t.svc.panels.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	// 端口与路径取装证书之前的：自签时期的地址带着它们
	byDomain, byAddress, err := domainBaseURL(srv.BaseURL, t.host.Domain)
	if err != nil {
		return err
	}
	if err := d.InstallCert(ctx, r, cert.CertPEM, cert.KeyPEM); err != nil {
		return err
	}
	// 证书已经换到面板上了：从这里起的收尾（记到期时间、必要时退回指纹接入）不能因为任务超时而半途而废，各自另起时限
	wctx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), taskWriteTimeout)
	defer cancelWrite()
	err = t.svc.q.SetServerHostCert(wctx, sqlcgen.SetServerHostCertParams{ID: t.host.ID, CertNotAfter: pgtype.Timestamptz{Time: cert.NotAfter, Valid: true}})
	if err != nil {
		return fmt.Errorf("save cert expiry of host %d: %w", t.host.ID, err)
	}
	verify := ServerConnInput{ProviderType: srv.ProviderType, BaseURL: byDomain, TLSMode: panel.TLSVerify}
	// 面板刚重启，要几秒才回来；ReplaceServerConn 探测通过才保存
	err = t.svc.waitFor(ctx, func() error { return t.svc.panels.ReplaceServerConn(ctx, t.adminID, serverID, verify) })
	if err == nil {
		t.logf("面板已换成公开证书，平台按 %s 接入", t.host.Domain)
		return nil
	}
	slog.Warn("panel unreachable by domain after installing certificate", "host_id", t.host.ID, "domain", t.host.Domain, "err", err)
	// 原来固定的指纹已经对不上新证书；先按新证书的指纹接入，保住在线
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), certFallbackTimeout)
	defer cancel()
	pinned := ServerConnInput{ProviderType: srv.ProviderType, BaseURL: byAddress, TLSMode: panel.TLSPin, CertPinSHA256: leafPin(cert.Leaf)}
	if err := t.svc.waitFor(fctx, func() error { return t.svc.panels.ReplaceServerConn(fctx, t.adminID, serverID, pinned) }); err != nil {
		return attachError(err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return deploy.NewError(deploy.ReasonPanelUnreachable, msgCertDomainDown)
}

// certify 给主机申请公开证书并装到面板上（docs/03 §5.22 “公开证书”，Ruling-73）。
func (t *taskRun) certify(ctx context.Context, d deploy.Deployer, r remote.Runner, serverID int64) error {
	cert, err := t.issueCert(ctx)
	if err != nil {
		slog.Warn("issue certificate", "host_id", t.host.ID, "domain", t.host.Domain, "err", err)
		return certError(err)
	}
	return withServerLock(serverID, func() error { return t.installCert(ctx, d, r, serverID, cert) })
}

// hasDNS 报告这台主机的面板证书是否由平台申请。
func (t *taskRun) hasDNS() bool { return t.host.DnsAccountID.Valid && t.host.Domain != "" }

// QueueCertRenewals 给证书快到期的主机各排一条“配置证书”任务（docs/03 §5.22 “证书自动续期”），返回排了几条。
// 任务没有发起人，审计记为系统；主机这时正好有了别的任务就跳过，下一轮再看。
func (s *HostService) QueueCertRenewals(ctx context.Context) (int, error) {
	due := pgtype.Timestamptz{Time: s.now().Add(s.cfg.RenewBefore), Valid: true}
	ids, err := s.q.ListHostsDueForCertRenewal(ctx, sqlcgen.ListHostsDueForCertRenewalParams{CertNotAfter: due, Limit: certRenewBatch})
	if err != nil {
		return 0, fmt.Errorf("list hosts due for cert renewal: %w", err)
	}
	queued := 0
	for _, hostID := range ids {
		err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
			_, err := s.insertTask(ctx, q, taskOrigin{actorType: ActorSystem}, hostID, HostTaskEnableTLS)
			return err
		})
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return queued, err
		}
		queued++
	}
	if queued > 0 {
		s.kickRunner()
	}
	return queued, nil
}
