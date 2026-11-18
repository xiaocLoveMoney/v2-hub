package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/remote"
)

// ServerHost 是托管主机视图。
type ServerHost struct {
	ID             int64
	Name           string
	Address        string
	SSHPort        int
	CredentialID   int64
	CredentialName string
	ProviderType   string
	Region         string
	// PanelPort 是安装时指定的面板端口；nil 表示随机。
	PanelPort     *int
	HostKeySHA256 string
	OSID          string
	Arch          string
	// ServerID 是已关联的服务器；nil 表示还没接入。
	ServerID   *int64
	ServerName string
	// DNSAccountID 是申请公开证书用的 DNS 账号；nil 表示不申请，面板用自签证书。Domain 与它同有同无。
	DNSAccountID   *int64
	DNSAccountName string
	Domain         string
	// CertNotAfter 是平台申请的证书的到期时间；nil 表示还没申请到。
	CertNotAfter *time.Time
	// LastTask 是最近一条任务；只在列表与详情里带，没有任务为 nil。
	LastTask  *HostTask
	CreatedAt time.Time
}

// ServerHostInput 是修改一台主机的输入。DNSAccountID 为 nil 表示不用 DNS 账号，此时 Domain 必须为空；
// 选了 DNS 账号而 Domain 为空时由平台按名称生成。
type ServerHostInput struct {
	Name         string
	Address      string
	SSHPort      int
	CredentialID int64
	Region       string
	PanelPort    *int
	DNSAccountID *int64
	Domain       string
}

// HostBatchItem 是批量新建里的一行；Domain 的规则同 ServerHostInput。
type HostBatchItem struct {
	Address string
	Name    string
	Region  string
	Domain  string
}

// HostBatchInput 是批量新建主机的输入：整批共用凭据、端口、面板类型与 DNS 账号。
type HostBatchInput struct {
	CredentialID int64
	SSHPort      int
	ProviderType string
	PanelPort    *int
	DNSAccountID *int64
	Hosts        []HostBatchItem
}

// HostTestInput 是用未保存的信息做连接测试的输入。
type HostTestInput struct {
	Address      string
	SSHPort      int
	CredentialID int64
	ProviderType string
}

// HostBindInput 是给已接入的服务器绑定 SSH 的输入。
type HostBindInput struct {
	Address      string
	SSHPort      int
	CredentialID int64
}

// HostDetection 是连接测试的结果。
type HostDetection struct {
	HostKeySHA256 string
	deploy.Detection
}

func hostFromRow(r sqlcgen.GetServerHostRow) ServerHost {
	h := ServerHost{
		ID: r.ID, Name: r.Name, Address: r.Address, SSHPort: int(r.SshPort), CredentialID: r.CredentialID, CredentialName: r.CredentialName,
		ProviderType: r.ProviderType, Region: r.Region, HostKeySHA256: r.HostKeySha256, OSID: r.OsID, Arch: r.Arch,
		ServerName: r.ServerName, CreatedAt: r.CreatedAt.Time,
		DNSAccountName: r.DnsAccountName, Domain: r.Domain, CertNotAfter: timePtr(r.CertNotAfter),
	}
	if r.DnsAccountID.Valid {
		id := r.DnsAccountID.Int64
		h.DNSAccountID = &id
	}
	if r.PanelPort.Valid {
		port := int(r.PanelPort.Int32)
		h.PanelPort = &port
	}
	if r.ServerID.Valid {
		id := r.ServerID.Int64
		h.ServerID = &id
	}
	return h
}

func (s *HostService) hostRow(ctx context.Context, id int64) (sqlcgen.GetServerHostRow, error) {
	row, err := s.q.GetServerHost(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, newError(CodeNotFound, msgHostNotFound)
	}
	if err != nil {
		return row, fmt.Errorf("get server host %d: %w", id, err)
	}
	return row, nil
}

// GetHost 返回一台主机及其最近一条任务。
func (s *HostService) GetHost(ctx context.Context, id int64) (*ServerHost, error) {
	row, err := s.hostRow(ctx, id)
	if err != nil {
		return nil, err
	}
	host := hostFromRow(row)
	tasks, err := s.listTasks(ctx, id, 1)
	if err != nil {
		return nil, err
	}
	if len(tasks) > 0 {
		host.LastTask = &tasks[0]
	}
	return &host, nil
}

// ListHosts 返回全部主机及各自最近一条任务。
func (s *HostService) ListHosts(ctx context.Context) ([]ServerHost, error) {
	rows, err := s.q.ListServerHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list server hosts: %w", err)
	}
	latest, err := s.q.ListLatestHostTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list latest host tasks: %w", err)
	}
	byHost := make(map[int64]HostTask, len(latest))
	for _, t := range latest {
		byHost[t.HostID] = taskFromListRow(sqlcgen.ListHostTasksRow(t))
	}
	out := make([]ServerHost, 0, len(rows))
	for _, r := range rows {
		host := hostFromRow(sqlcgen.GetServerHostRow(r))
		if t, ok := byHost[host.ID]; ok {
			host.LastTask = &t
		}
		out = append(out, host)
	}
	return out, nil
}

// checkProviderType 要求面板类型既能部署也能接入。
func (s *HostService) checkProviderType(providerType string) error {
	if _, ok := s.deployers.Get(providerType); !ok || !s.panels.registry.Has(providerType) {
		return newError(CodeInvalidArgument, msgBadProviderType+providerType)
	}
	return nil
}

func validatePanelPort(port *int) error {
	if port == nil {
		return nil
	}
	return validatePort(*port)
}

// normalizeEndpoint 给 SSH 端口补默认值并校验，再规范化主机地址。
func (s *HostService) normalizeEndpoint(address string, port int) (string, int, error) {
	if port == 0 {
		port = defaultSSHPort
	}
	if err := validatePort(port); err != nil {
		return "", 0, err
	}
	address, err := normalizeHostAddress(address, s.cfg.AllowLoopback)
	return address, port, err
}

// normalizeBatch 校验整批输入并规范化每一行；任一行不合法即整批拒绝，错误里指出是第几行。
func (s *HostService) normalizeBatch(in HostBatchInput) (HostBatchInput, error) {
	if in.SSHPort == 0 {
		in.SSHPort = defaultSSHPort
	}
	if err := validatePort(in.SSHPort); err != nil {
		return in, err
	}
	if err := validatePanelPort(in.PanelPort); err != nil {
		return in, err
	}
	if err := s.checkProviderType(in.ProviderType); err != nil {
		return in, err
	}
	if len(in.Hosts) == 0 || len(in.Hosts) > s.cfg.MaxBatch {
		return in, newError(CodeInvalidArgument, fmt.Sprintf("一次须填 1–%d 台主机", s.cfg.MaxBatch))
	}
	names := make(map[string]struct{}, len(in.Hosts))
	addresses := make(map[string]struct{}, len(in.Hosts))
	hosts := make([]HostBatchItem, 0, len(in.Hosts))
	for i, item := range in.Hosts {
		row := i + 1
		address, err := normalizeHostAddress(item.Address, s.cfg.AllowLoopback)
		if err != nil {
			return in, rowError(row, err)
		}
		name, err := validateName(item.Name)
		if err != nil {
			return in, rowError(row, err)
		}
		if err := validateServerRegion(item.Region); err != nil {
			return in, rowError(row, err)
		}
		_, dupName := names[name]
		_, dupAddress := addresses[address]
		if dupName || dupAddress {
			return in, newError(CodeInvalidArgument, fmt.Sprintf("第 %d 行与前面的行重名或地址重复", row))
		}
		names[name], addresses[address] = struct{}{}, struct{}{}
		hosts = append(hosts, HostBatchItem{Address: address, Name: name, Region: item.Region, Domain: item.Domain})
	}
	in.Hosts = hosts
	return in, nil
}

func rowError(row int, err error) error {
	var se *Error
	if errors.As(err, &se) {
		return newError(se.Code, fmt.Sprintf("第 %d 行：%s", row, se.Message))
	}
	return err
}

// CreateHosts 批量新建主机并各排一条部署任务；整批在一个事务里，任一行冲突都不保存。
func (s *HostService) CreateHosts(ctx context.Context, actor *AdminPrincipal, in HostBatchInput, ip *netip.Addr) ([]ServerHost, error) {
	in, err := s.normalizeBatch(in)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(in.Hosts))
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		for _, item := range in.Hosts {
			id, err := s.createHost(ctx, q, actor, ip, createHostParams{
				item: item, credentialID: in.CredentialID, sshPort: in.SSHPort, providerType: in.ProviderType,
				panelPort: in.PanelPort, dnsAccountID: in.DNSAccountID, kind: HostTaskProvision, action: ActionHostCreate,
			})
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.kickRunner()
	out := make([]ServerHost, 0, len(ids))
	for _, id := range ids {
		host, err := s.GetHost(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *host)
	}
	return out, nil
}

type createHostParams struct {
	item         HostBatchItem
	credentialID int64
	sshPort      int
	providerType string
	panelPort    *int
	// dnsAccountID 为 nil 表示不申请公开证书。
	dnsAccountID *int64
	// serverID 非 0 表示给已接入的服务器绑定。
	serverID int64
	kind     string
	action   string
}

// createHost 在事务里建一台主机、排它的第一条任务并写审计。
func (s *HostService) createHost(ctx context.Context, q *sqlcgen.Queries, actor *AdminPrincipal, ip *netip.Addr, p createHostParams) (int64, error) {
	if p.serverID == 0 {
		// 部署成功后主机名就是服务器名，先挡掉与现有服务器重名的
		n, err := q.CountServersByName(ctx, p.item.Name)
		if err != nil {
			return 0, fmt.Errorf("count servers by name: %w", err)
		}
		if n > 0 {
			return 0, newError(CodeConflict, msgHostNameIsServer+"："+p.item.Name)
		}
	}
	dnsAccountID, domain, err := hostDomain(ctx, q, p.dnsAccountID, p.item.Domain, p.item.Name, p.item.Address)
	if err != nil {
		return 0, err
	}
	id, err := q.CreateServerHost(ctx, sqlcgen.CreateServerHostParams{
		Name: p.item.Name, Address: p.item.Address, SshPort: int32(p.sshPort), //nolint:gosec // 端口已校验在 1–65535
		CredentialID: p.credentialID, ProviderType: p.providerType, Region: p.item.Region,
		PanelPort: int4OrNull(p.panelPort), ServerID: pgtype.Int8{Int64: p.serverID, Valid: p.serverID != 0},
		DnsAccountID: dnsAccountID, Domain: domain,
	})
	if isUniqueViolation(err) {
		if p.serverID != 0 {
			return 0, newError(CodeConflict, msgServerHasHost+"，或"+msgHostTaken)
		}
		return 0, newError(CodeConflict, msgHostTaken+"："+p.item.Name)
	}
	if isForeignKeyViolation(err) {
		return 0, newError(CodeNotFound, msgCredentialNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("create server host: %w", err)
	}
	if _, err := s.insertTask(ctx, q, adminOrigin(actor, ip), id, p.kind); err != nil {
		return 0, err
	}
	return id, writeAudit(ctx, q, AuditEntry{
		ActorType: ActorAdmin, ActorID: actor.AdminID, Action: p.action, TargetType: targetHost, TargetID: fmt.Sprint(id), ClientIP: ip,
		Detail: map[string]any{"name": p.item.Name, "address": p.item.Address, "sshPort": p.sshPort, "credentialId": p.credentialID, "serverId": p.serverID, "domain": domain},
	})
}

func (s *HostService) ensureIdle(ctx context.Context, q *sqlcgen.Queries, hostID int64) error {
	n, err := q.CountActiveHostTasks(ctx, hostID)
	if err != nil {
		return fmt.Errorf("count active tasks of host %d: %w", hostID, err)
	}
	if n > 0 {
		return newError(CodeConflict, msgHostBusy)
	}
	return nil
}

// UpdateHost 修改主机；改了地址或端口会清空已固定的主机密钥。有未结束的任务时返回 409。
func (s *HostService) UpdateHost(ctx context.Context, actor *AdminPrincipal, id int64, in ServerHostInput, ip *netip.Addr) (*ServerHost, error) {
	name, err := validateName(in.Name)
	if err != nil {
		return nil, err
	}
	address, err := normalizeHostAddress(in.Address, s.cfg.AllowLoopback)
	if err != nil {
		return nil, err
	}
	if err := validatePort(in.SSHPort); err != nil {
		return nil, err
	}
	if err := validatePanelPort(in.PanelPort); err != nil {
		return nil, err
	}
	if err := validateServerRegion(in.Region); err != nil {
		return nil, err
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := s.ensureIdle(ctx, q, id); err != nil {
			return err
		}
		dnsAccountID, domain, err := hostDomain(ctx, q, in.DNSAccountID, in.Domain, name, address)
		if err != nil {
			return err
		}
		n, err := q.UpdateServerHost(ctx, sqlcgen.UpdateServerHostParams{
			ID: id, Name: name, Address: address, SshPort: int32(in.SSHPort), //nolint:gosec // 端口已校验在 1–65535
			CredentialID: in.CredentialID, Region: in.Region, PanelPort: int4OrNull(in.PanelPort),
			DnsAccountID: dnsAccountID, Domain: domain, Now: pgtype.Timestamptz{Time: s.now(), Valid: true},
		})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgHostTaken)
		}
		if isForeignKeyViolation(err) {
			return newError(CodeNotFound, msgCredentialNotFound)
		}
		if err != nil {
			return fmt.Errorf("update server host %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgHostNotFound)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionHostUpdate, TargetType: targetHost, TargetID: fmt.Sprint(id), ClientIP: ip,
			Detail: map[string]any{"name": name, "address": address, "sshPort": in.SSHPort, "credentialId": in.CredentialID, "domain": domain},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.GetHost(ctx, id)
}

// DeleteHost 删除主机的平台记录与任务历史，不动目标机与关联的服务器。有未结束的任务时返回 409。
func (s *HostService) DeleteHost(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := s.ensureIdle(ctx, q, id); err != nil {
			return err
		}
		n, err := q.DeleteServerHost(ctx, id)
		if err != nil {
			return fmt.Errorf("delete server host %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgHostNotFound)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionHostDelete, TargetType: targetHost, TargetID: fmt.Sprint(id), ClientIP: ip,
		})
	})
}

// ResetHostKey 清空已固定的主机密钥，下次连接重新记录。
func (s *HostService) ResetHostKey(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) (*ServerHost, error) {
	if _, err := s.hostRow(ctx, id); err != nil {
		return nil, err
	}
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.SetServerHostKey(ctx, sqlcgen.SetServerHostKeyParams{ID: id, HostKeySha256: ""}); err != nil {
			return fmt.Errorf("reset host key of %d: %w", id, err)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionHostResetHostKey, TargetType: targetHost, TargetID: fmt.Sprint(id), ClientIP: ip,
		})
	})
	if err != nil {
		return nil, err
	}
	return s.GetHost(ctx, id)
}

// BindServer 给已接入的服务器绑定 SSH，并排一条校验连接的任务；主机名取服务器名。
func (s *HostService) BindServer(ctx context.Context, actor *AdminPrincipal, serverID int64, in HostBindInput, ip *netip.Addr) (*ServerHost, error) {
	address, sshPort, err := s.normalizeEndpoint(in.Address, in.SSHPort)
	if err != nil {
		return nil, err
	}
	srv, err := s.panels.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if err := s.checkProviderType(srv.ProviderType); err != nil {
		return nil, err
	}
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		id, err = s.createHost(ctx, q, actor, ip, createHostParams{
			item: HostBatchItem{Address: address, Name: srv.Name, Region: srv.Region}, credentialID: in.CredentialID, sshPort: sshPort,
			providerType: srv.ProviderType, serverID: serverID, kind: HostTaskVerify, action: ActionHostBind,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	s.kickRunner()
	return s.GetHost(ctx, id)
}

// dial 解密凭据并连上主机；pinnedKey 为空表示首次连接。调用方用完须关闭。
func (s *HostService) dial(ctx context.Context, address string, port int, credentialID int64, pinnedKey string) (remote.Runner, error) {
	cred, err := s.openCredential(ctx, credentialID)
	if err != nil {
		return nil, err
	}
	target := remote.Target{Address: address, Port: port, Username: cred.username, HostKeySHA256: pinnedKey}
	if cred.authType == SSHAuthPassword {
		target.Password = string(cred.secret)
	} else {
		target.PrivateKey, target.Passphrase = cred.secret, cred.passphrase
	}
	return s.dialer.Dial(ctx, target)
}

// endpoint 是一次连接的去处。
type endpoint struct {
	address      string
	port         int
	credentialID int64
	pinnedKey    string
}

// withRunner 给“请求里同步做一件事”的接口用：在普通动作的时限内连上主机、执行 fn、关闭连接，
// SSH 与部署层的错误统一转成对外的 502。
func (s *HostService) withRunner(ctx context.Context, e endpoint, fn func(ctx context.Context, r remote.Runner) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.CommandTimeout)
	defer cancel()
	runner, err := s.dial(ctx, e.address, e.port, e.credentialID, e.pinnedKey)
	if err != nil {
		return upstreamError(err)
	}
	defer closeRunner(runner)
	return upstreamError(fn(ctx, runner))
}

// TestHost 用未保存的信息连一次主机并检测；不落库，按管理员频控。SSH 失败返回 502。
func (s *HostService) TestHost(ctx context.Context, actor *AdminPrincipal, in HostTestInput) (*HostDetection, error) {
	address, sshPort, err := s.normalizeEndpoint(in.Address, in.SSHPort)
	if err != nil {
		return nil, err
	}
	deployer, ok := s.deployers.Get(in.ProviderType)
	if !ok {
		return nil, newError(CodeInvalidArgument, msgBadProviderType+in.ProviderType)
	}
	if err := allow(s.tests, actor.AdminID); err != nil {
		return nil, err
	}
	var out HostDetection
	err = s.withRunner(ctx, endpoint{address: address, port: sshPort, credentialID: in.CredentialID}, func(ctx context.Context, r remote.Runner) error {
		det, err := deployer.Detect(ctx, r)
		out = HostDetection{HostKeySHA256: r.HostKeySHA256(), Detection: det}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// upstreamError 把 SSH 与部署层的错误转成对外的 502，message 取脱敏后的原因；其它错误（含 nil）原样返回。
func upstreamError(err error) error {
	if _, message, ok := deploy.FailureOf(err); ok {
		return newError(CodeUpstream, message)
	}
	return err
}
