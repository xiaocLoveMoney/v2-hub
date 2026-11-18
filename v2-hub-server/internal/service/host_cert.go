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

	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
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
	// certRenewFallback 是读不到 acme.sh 的下次续期时间时，按离到期还剩多久开始续期。
	certRenewFallback = 30 * 24 * time.Hour
	// certFallbackTimeout 是“按域名连不上、退回指纹接入”这一步自己的时限：走到这里时任务的时限可能已经用完，
	// 而证书已经换了，不退回的话服务器会一直对不上原来固定的指纹。
	certFallbackTimeout = time.Minute

	msgCertDomainDown  = "证书已装好，但平台按域名连不上面板（解析可能还没传到平台所在的网络），暂按证书指纹接入；下一轮检查会自动重试"
	msgCertAfterAttach = "面板已接入，但公开证书没有申请成功；下一轮检查会自动重试，也可以排除问题后在 SSH 运维里重做“配置证书”，不需要重新部署"
)

// certError 把 DNS 服务商的错误归到 dns_failed；部署层的错误（含 cert_failed）与其它错误原样返回。
func certError(err error) error {
	if de := dns.AsError(err); de != nil {
		return deploy.NewError(deploy.ReasonDNSFailed, de.Message)
	}
	return err
}

func leafPin(leaf []byte) string {
	sum := sha256.Sum256(leaf)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// issueCert 做“公开证书”的 ①②：把域名解析到主机，再在主机上用 acme.sh 签发（Ruling-74）。这一段可能要几分钟，不占对账锁。
func (t *taskRun) issueCert(ctx context.Context, d deploy.Deployer, r remote.Runner) error {
	host := t.host
	account, err := t.svc.openDNSAccount(ctx, host.DnsAccountID.Int64)
	if err != nil {
		return err
	}
	t.secrets = append(t.secrets, account.token)
	provider, err := t.svc.dns.Open(account.providerType, dns.Account{Zone: account.zone, Token: account.token})
	if err != nil {
		return fmt.Errorf("open dns provider %s: %w", account.providerType, err)
	}
	if host.Domain == host.Address {
		t.step(ctx, hostStepDNS, fmt.Sprintf("域名 %s 就是主机地址，不改解析", host.Domain))
	} else {
		t.step(ctx, hostStepDNS, fmt.Sprintf("把 %s 解析到 %s", host.Domain, host.Address))
		if err := provider.PointTo(ctx, host.Domain, host.Address); err != nil {
			return err
		}
	}
	t.step(ctx, hostStepIssue, "在主机上用 acme.sh 申请证书（DNS 验证，通常要一两分钟）")
	hook := provider.ACMEHook()
	for _, value := range hook.Env {
		t.secrets = append(t.secrets, value)
	}
	req := deploy.CertRequest{Domain: host.Domain, DirectoryURL: t.svc.cfg.ACMEDirectoryURL, DNSHook: hook.Name, Env: hook.Env}
	reused, err := d.IssueCert(ctx, r, req, func(line string) { t.logf("acme.sh: %s", line) })
	if err != nil {
		return err
	}
	if reused {
		t.logf("主机上已有这个域名的证书且还没到续期时间，直接复用")
		return nil
	}
	t.logf("证书已签出")
	return nil
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

// installCert 做“公开证书”的 ③④⑤：把证书设给面板，把服务器改成按域名接入，接入成功后记下到期与续期时间。
// 调用方须已占用该服务器的对账锁：这一段可能重启面板并改接入信息。
func (t *taskRun) installCert(ctx context.Context, d deploy.Deployer, r remote.Runner, serverID int64) error {
	t.step(ctx, hostStepInstallCert, "把证书设给面板")
	srv, err := t.svc.panels.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	// 端口与路径取装证书之前的：自签时期的地址带着它们
	byDomain, byAddress, err := domainBaseURL(srv.BaseURL, t.host.Domain)
	if err != nil {
		return err
	}
	cert, err := d.InstallCert(ctx, r, t.host.Domain)
	if err != nil {
		return err
	}
	if cert.Changed {
		t.logf("面板已换上证书并重启，证书到期 %s", cert.NotAfter.UTC().Format(time.DateOnly))
	} else {
		t.logf("面板上已经是这张证书，没有重启")
	}
	verify := ServerConnInput{ProviderType: srv.ProviderType, BaseURL: byDomain, TLSMode: panel.TLSVerify}
	// 面板刚重启，要几秒才回来；ReplaceServerConn 探测通过才保存
	err = t.svc.waitFor(ctx, func() error { return t.svc.panels.ReplaceServerConn(ctx, t.adminID, serverID, verify) })
	if err == nil {
		t.logf("平台按 %s 接入", t.host.Domain)
		return t.saveCertDates(ctx, cert)
	}
	slog.Warn("panel unreachable by domain after installing certificate", "host_id", t.host.ID, "domain", t.host.Domain, "err", err)
	if !cert.Changed && srv.TLSMode == panel.TLSVerify {
		// 面板证书没动、原来就按域名接入：探测不通是别的原因，接入信息保持原样
		return attachError(err)
	}
	// 证书已经换了：原来固定的指纹对不上新证书，先按新证书的指纹接入、保住在线；收尾不能因为任务超时而半途而废，另起时限
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), certFallbackTimeout)
	defer cancel()
	pinned := ServerConnInput{ProviderType: srv.ProviderType, BaseURL: byAddress, TLSMode: panel.TLSPin, CertPinSHA256: leafPin(cert.LeafDER)}
	if err := t.svc.waitFor(fctx, func() error { return t.svc.panels.ReplaceServerConn(fctx, t.adminID, serverID, pinned) }); err != nil {
		return attachError(err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return deploy.NewError(deploy.ReasonPanelUnreachable, msgCertDomainDown)
}

// saveCertDates 记下证书的到期时间与下次续期时间；读不到 acme.sh 的续期时间时按到期前 certRenewFallback。
func (t *taskRun) saveCertDates(ctx context.Context, cert deploy.InstalledCert) error {
	renewAt := cert.RenewAt
	if renewAt.IsZero() {
		renewAt = cert.NotAfter.Add(-certRenewFallback)
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskWriteTimeout)
	defer cancel()
	err := t.svc.q.SetServerHostCert(wctx, sqlcgen.SetServerHostCertParams{
		ID: t.host.ID, CertNotAfter: pgtype.Timestamptz{Time: cert.NotAfter, Valid: true}, CertRenewAt: pgtype.Timestamptz{Time: renewAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("save cert dates of host %d: %w", t.host.ID, err)
	}
	t.logf("下次续期时间 %s", renewAt.UTC().Format(time.DateTime))
	return nil
}

// certify 给主机申请公开证书并装到面板上（docs/03 §5.22 “公开证书”，Ruling-74）。
// 没成功时把续期时间提前到现在，下一轮检查自动重试（主机上已签好的证书会被复用，不重签）。
func (t *taskRun) certify(ctx context.Context, d deploy.Deployer, r remote.Runner, serverID int64) error {
	err := t.issueCert(ctx, d, r)
	if err != nil {
		slog.Warn("issue certificate", "host_id", t.host.ID, "domain", t.host.Domain, "err", err)
		err = certError(err)
	} else {
		err = withServerLock(serverID, func() error { return t.installCert(ctx, d, r, serverID) })
	}
	if err != nil {
		t.markCertRetry(ctx)
	}
	return err
}

func (t *taskRun) markCertRetry(ctx context.Context) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskWriteTimeout)
	defer cancel()
	now := pgtype.Timestamptz{Time: t.svc.now(), Valid: true}
	if err := t.svc.q.MarkServerHostCertRetry(wctx, sqlcgen.MarkServerHostCertRetryParams{ID: t.host.ID, CertRenewAt: now}); err != nil {
		slog.Warn("mark host cert retry", "host_id", t.host.ID, "err", err)
	}
}

// hasDNS 报告这台主机的面板证书是否由平台申请。
func (t *taskRun) hasDNS() bool { return hostHasDNS(t.host) }

// hostHasDNS 报告主机是否选了 DNS 账号并填了域名（平台才能给它申请证书）。
func hostHasDNS(host sqlcgen.GetServerHostRow) bool {
	return host.DnsAccountID.Valid && host.Domain != ""
}

// QueueCertRenewals 给已到续期时间的主机各排一条“配置证书”任务（docs/03 §5.22 “证书自动续期”），返回排了几条。
// 任务没有发起人，审计记为系统；主机这时正好有了别的任务就跳过，下一轮再看；单台排不进去不影响其它主机。
func (s *HostService) QueueCertRenewals(ctx context.Context) (int, error) {
	due := pgtype.Timestamptz{Time: s.now(), Valid: true}
	ids, err := s.q.ListHostsDueForCertRenewal(ctx, sqlcgen.ListHostsDueForCertRenewalParams{CertRenewAt: due, Limit: certRenewBatch})
	if err != nil {
		return 0, fmt.Errorf("list hosts due for cert renewal: %w", err)
	}
	queued := 0
	var errs []error
	for _, hostID := range ids {
		err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
			_, err := s.insertTask(ctx, q, taskOrigin{actorType: ActorSystem}, hostID, HostTaskEnableTLS)
			return err
		})
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("queue cert renewal of host %d: %w", hostID, err))
			continue
		}
		queued++
	}
	if queued > 0 {
		s.kickRunner()
	}
	return queued, errors.Join(errs...)
}
