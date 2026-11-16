package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

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
	// LastTask 是最近一条任务；只在列表与详情里带，没有任务为 nil。
	LastTask  *HostTask
	CreatedAt time.Time
}

// ServerHostInput 是修改一台主机的输入。
type ServerHostInput struct {
	Name         string
	Address      string
	SSHPort      int
	CredentialID int64
	Region       string
	PanelPort    *int
}

// HostBatchItem 是批量新建里的一行。
type HostBatchItem struct {
	Address string
	Name    string
	Region  string
}

// HostBatchInput 是批量新建主机的输入：整批共用凭据、端口与面板类型。
type HostBatchInput struct {
	CredentialID int64
	SSHPort      int
	ProviderType string
	PanelPort    *int
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
	tasks, err := s.ListTasks(ctx, id, 1)
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

func validateHostName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxServerNameLen {
		return "", newError(CodeInvalidArgument, fmt.Sprintf("名称长度须为 1–%d", maxServerNameLen))
	}
	return name, nil
}

func validatePanelPort(port *int) error {
	if port == nil {
		return nil
	}
	return validatePort(*port)
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
		name, err := validateHostName(item.Name)
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
		hosts = append(hosts, HostBatchItem{Address: address, Name: name, Region: item.Region})
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
	if _, err := s.getCredential(ctx, in.CredentialID); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(in.Hosts))
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		for _, item := range in.Hosts {
			id, err := s.createHost(ctx, q, actor, ip, createHostParams{
				item: item, credentialID: in.CredentialID, sshPort: in.SSHPort, providerType: in.ProviderType,
				panelPort: in.PanelPort, kind: HostTaskProvision, action: ActionHostCreate,
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
	id, err := q.CreateServerHost(ctx, sqlcgen.CreateServerHostParams{
		Name: p.item.Name, Address: p.item.Address, SshPort: int32(p.sshPort), //nolint:gosec // 端口已校验在 1–65535
		CredentialID: p.credentialID, ProviderType: p.providerType, Region: p.item.Region,
		PanelPort: int4OrNull(p.panelPort), ServerID: pgtype.Int8{Int64: p.serverID, Valid: p.serverID != 0},
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
	if _, err := s.insertTask(ctx, q, actor, ip, id, p.kind); err != nil {
		return 0, err
	}
	return id, writeAudit(ctx, q, AuditEntry{
		ActorType: ActorAdmin, ActorID: actor.AdminID, Action: p.action, TargetType: targetHost, TargetID: fmt.Sprint(id), ClientIP: ip,
		Detail: map[string]any{"name": p.item.Name, "address": p.item.Address, "sshPort": p.sshPort, "credentialId": p.credentialID, "serverId": p.serverID},
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
	name, err := validateHostName(in.Name)
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
		n, err := q.UpdateServerHost(ctx, sqlcgen.UpdateServerHostParams{
			ID: id, Name: name, Address: address, SshPort: int32(in.SSHPort), //nolint:gosec // 端口已校验在 1–65535
			CredentialID: in.CredentialID, Region: in.Region, PanelPort: int4OrNull(in.PanelPort),
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
			Detail: map[string]any{"name": name, "address": address, "sshPort": in.SSHPort, "credentialId": in.CredentialID},
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
	if in.SSHPort == 0 {
		in.SSHPort = defaultSSHPort
	}
	if err := validatePort(in.SSHPort); err != nil {
		return nil, err
	}
	address, err := normalizeHostAddress(in.Address, s.cfg.AllowLoopback)
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
			item: HostBatchItem{Address: address, Name: srv.Name, Region: srv.Region}, credentialID: in.CredentialID, sshPort: in.SSHPort,
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

// dialTarget 组出连接主机用的全部信息；pinnedKey 为空表示首次连接。
func (s *HostService) dialTarget(ctx context.Context, address string, port int, credentialID int64, pinnedKey string) (remote.Target, error) {
	cred, err := s.openCredential(ctx, credentialID)
	if err != nil {
		return remote.Target{}, err
	}
	target := remote.Target{Address: address, Port: port, Username: cred.username, HostKeySHA256: pinnedKey}
	if cred.authType == SSHAuthPassword {
		target.Password = string(cred.secret)
		return target, nil
	}
	target.PrivateKey, target.Passphrase = cred.secret, cred.passphrase
	return target, nil
}

// TestHost 用未保存的信息连一次主机并检测；不落库，按管理员频控。SSH 失败返回 502。
func (s *HostService) TestHost(ctx context.Context, actor *AdminPrincipal, in HostTestInput) (*HostDetection, error) {
	if in.SSHPort == 0 {
		in.SSHPort = defaultSSHPort
	}
	if err := validatePort(in.SSHPort); err != nil {
		return nil, err
	}
	address, err := normalizeHostAddress(in.Address, s.cfg.AllowLoopback)
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
	target, err := s.dialTarget(ctx, address, in.SSHPort, in.CredentialID, "")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.CommandTimeout)
	defer cancel()
	runner, err := s.dialer.Dial(ctx, target)
	if err != nil {
		return nil, upstreamError(err)
	}
	defer closeRunner(runner)
	det, err := deployer.Detect(ctx, runner)
	if err != nil {
		return nil, upstreamError(err)
	}
	return &HostDetection{HostKeySHA256: runner.HostKeySHA256(), Detection: det}, nil
}

// upstreamError 把 SSH 与部署层的错误转成对外的 502，message 取脱敏后的原因。
func upstreamError(err error) error {
	if re := remote.AsError(err); re != nil {
		return newError(CodeUpstream, re.Message)
	}
	if de := deploy.AsError(err); de != nil {
		return newError(CodeUpstream, de.Message)
	}
	return err
}

// panelHost 取服务器面板地址里的主机名，给证书主体用。
func panelHost(baseURL string) (*url.URL, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("parse panel base url")
	}
	return u, nil
}
