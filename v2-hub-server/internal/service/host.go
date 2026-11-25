package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/dns"
	"v2-hub-server/internal/manager/ratelimit"
	"v2-hub-server/internal/manager/remote"
)

// SSH 认证方式（docs/03 §5.22）。
const (
	SSHAuthKey      = "key"
	SSHAuthPassword = "password"
)

const (
	maxSSHUsernameLen   = 64
	maxSSHKeyBytes      = 16 << 10
	maxSSHPasswordLen   = 255
	maxSSHPassphraseLen = 255
	defaultSSHPort      = 22
	hostGuardMaxKeys    = 1024
	// defaultConfirmMaxFailures / defaultConfirmWindow：密码凭据的确认输错频控的默认值（docs/03 §5.22，锁定时长同窗口）
	defaultConfirmMaxFailures = 5
	defaultConfirmWindow      = 15 * time.Minute
	localhostName             = "localhost"

	ActionSSHCredentialCreate = "ssh_credential.create"
	ActionSSHCredentialUpdate = "ssh_credential.update"
	ActionSSHCredentialDelete = "ssh_credential.delete"
	ActionHostCreate          = "host.create"
	ActionHostUpdate          = "host.update"
	ActionHostDelete          = "host.delete"
	ActionHostResetHostKey    = "host.reset_host_key"
	ActionHostBind            = "host.bind"
	ActionHostPanelLoginReset = "host.panel_login_reset"
	ActionHostTaskCreate      = "host_task.create"
	ActionHostTaskCancel      = "host_task.cancel"
	targetSSHCredential       = "ssh_credential"
	targetHost                = "server_host"
	targetHostTask            = "host_task"

	msgCredentialNotFound  = "SSH 凭据不存在"
	msgCredentialInUse     = "还有主机在用这份凭据，不能删除"
	msgCredentialNameTaken = "凭据名称已存在"
	msgHostNotFound        = "主机不存在"
	msgHostBusy            = "该主机有未结束的任务"
	msgHostTaken           = "主机名称、“地址 + 端口”或域名已存在"
	msgHostNameIsServer    = "已有同名的服务器"
	msgHostRateLimited     = "操作太频繁，请稍后再试"
	msgServerHasHost       = "这台服务器已绑定 SSH"
	msgBadHostAddress      = "地址须为 IP 或域名"
	msgLoopbackAddress     = "不能使用回环或未指定地址"
	msgBadPort             = "端口须在 1–65535 之间"
	msgBadProviderType     = "不支持的面板类型："
)

var sshUsernamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]*$`)

// HostConfig 是 SSH 托管业务的参数（docs/03 §5.22）。
type HostConfig struct {
	// CommandTimeout 是普通动作的总时限，InstallTimeout 是部署与升级的总时限。
	CommandTimeout time.Duration
	InstallTimeout time.Duration
	// TestLimit / TestWindow 是每个管理员做连接测试的频率；ActionLimit / ActionWindow 是发起动作的频率。
	TestLimit    int
	TestWindow   time.Duration
	ActionLimit  int
	ActionWindow time.Duration
	// MaxBatch 是一次批量新建的主机数上限。
	MaxBatch int
	// AllowLoopback 允许回环地址，只给联调用。
	AllowLoopback bool
	// CertTimeout 是申请公开证书的任务的总时限（改解析、主机上签发、装证书）；ACMEDirectoryURL 是传给主机上 acme.sh 的签发方目录地址。
	CertTimeout      time.Duration
	ACMEDirectoryURL string
	// PanelWaitAttempts / PanelWaitInterval 是面板重启后等它回来的探测次数与间隔。
	PanelWaitAttempts int
	PanelWaitInterval time.Duration
	// ConfirmMaxFailures / ConfirmWindow / ConfirmLockout 是“密码凭据的确认”输错的频控（按管理员，Ruling-97）；
	// 任一项不大于 0 时取 docs/03 §5.22 的默认值（15 分钟内 5 次、锁 15 分钟）。
	ConfirmMaxFailures int
	ConfirmWindow      time.Duration
	ConfirmLockout     time.Duration
	// Now 为 nil 时用 time.Now。
	Now func() time.Time
}

// Reconciler 请求尽快做一次下发对账（由 UserService 实现）。
type Reconciler interface {
	TriggerReconcile()
}

// HostDeps 是 HostService 依赖的外部能力；都以接口或注册表的形式注入，单测里换成假的。
type HostDeps struct {
	Dialer    remote.Dialer
	Deployers *deploy.Registry
	// DNS 是 DNS 服务商的注册表（Ruling-72）。
	DNS    *dns.Registry
	Panels *PanelService
	// Reconciler 在一键部署建完入站后请求一次下发对账（Ruling-76）；nil 时只等定时对账。
	Reconciler Reconciler
}

// HostService 负责 SSH 凭据、DNS 账号、托管主机与主机任务（docs/03 §5.22，Ruling-68 – Ruling-74）。
// 只经 remote.Dialer 连主机、经 deploy.Deployer 执行动作，自己不拼任何命令行。
type HostService struct {
	store
	sealer    *crypto.Sealer
	dialer    remote.Dialer
	deployers *deploy.Registry
	dns       *dns.Registry
	panels    *PanelService
	// reconciler 可为 nil。
	reconciler Reconciler
	cfg        HostConfig
	tests      *ratelimit.FailureGuard
	actions    *ratelimit.FailureGuard
	// confirms 统计“密码凭据的确认”输错的次数（Ruling-97）。
	confirms *ratelimit.FailureGuard
	// logins 保证同一台主机同时只有一次“重置面板登录口令”。
	logins *exitTestLocks
	kick   chan struct{}
	now    func() time.Time
}

// NewHostService 创建 SSH 托管业务服务。
func NewHostService(pool *pgxpool.Pool, sealer *crypto.Sealer, deps HostDeps, cfg HostConfig) *HostService {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	guard := func(limit int, window time.Duration) *ratelimit.FailureGuard {
		return ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{MaxFailures: limit, Window: window, Lockout: window, MaxKeys: hostGuardMaxKeys}, now)
	}
	confirm := ratelimit.FailureGuardConfig{
		MaxFailures: cfg.ConfirmMaxFailures, Window: cfg.ConfirmWindow, Lockout: cfg.ConfirmLockout, MaxKeys: hostGuardMaxKeys,
	}
	if confirm.MaxFailures <= 0 || confirm.Window <= 0 || confirm.Lockout <= 0 {
		confirm.MaxFailures, confirm.Window, confirm.Lockout = defaultConfirmMaxFailures, defaultConfirmWindow, defaultConfirmWindow
	}
	return &HostService{
		store: newStore(pool), sealer: sealer, dialer: deps.Dialer, deployers: deps.Deployers, dns: deps.DNS,
		panels: deps.Panels, reconciler: deps.Reconciler, cfg: cfg,
		tests: guard(cfg.TestLimit, cfg.TestWindow), actions: guard(cfg.ActionLimit, cfg.ActionWindow),
		confirms: ratelimit.NewFailureGuard(confirm, now),
		logins:   newExitTestLocks(), kick: make(chan struct{}, 1), now: now,
	}
}

// Requests 在有新任务排队时发信号，后台任务据此立即领取。
func (s *HostService) Requests() <-chan struct{} { return s.kick }

// kickRunner 非阻塞地提醒后台任务；短时间内的多次提醒合并成一次。
func (s *HostService) kickRunner() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func allow(guard *ratelimit.FailureGuard, adminID int64) error {
	key := strconv.FormatInt(adminID, 10)
	if guard.IsLocked(key) {
		return newError(CodeRateLimited, msgHostRateLimited)
	}
	guard.RecordFailure(key)
	return nil
}

// SSHCredential 是 SSH 凭据视图，不含私钥、密码与口令。
type SSHCredential struct {
	ID             int64
	Name           string
	Username       string
	AuthType       string
	KeyFingerprint string
	Remark         string
	HostCount      int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SSHCredentialInput 是新建 / 修改凭据的输入。Secret、Passphrase 为空表示沿用原值（仅修改时）。
type SSHCredentialInput struct {
	Name       string
	Username   string
	AuthType   string
	Secret     string
	Passphrase string
	Remark     string
}

func secretAAD(id int64) []byte     { return fmt.Appendf(nil, "ssh_credential:%d:secret", id) }
func passphraseAAD(id int64) []byte { return fmt.Appendf(nil, "ssh_credential:%d:passphrase", id) }

// validateName 去掉首尾空白并校验凭据 / 主机的名称长度。
func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxServerNameLen {
		return "", newError(CodeInvalidArgument, fmt.Sprintf("名称长度须为 1–%d", maxServerNameLen))
	}
	return name, nil
}

// validateCredentialFields 校验并规范化凭据的非密文字段；密码认证没有私钥口令，一并清掉。
func validateCredentialFields(in SSHCredentialInput) (SSHCredentialInput, error) {
	name, err := validateName(in.Name)
	if err != nil {
		return in, err
	}
	in.Name = name
	in.Username = strings.TrimSpace(in.Username)
	if in.AuthType == SSHAuthPassword {
		in.Passphrase = ""
	}
	if len(in.Username) > maxSSHUsernameLen || !sshUsernamePattern.MatchString(in.Username) {
		return in, newError(CodeInvalidArgument, "登录用户名不合法")
	}
	if in.AuthType != SSHAuthKey && in.AuthType != SSHAuthPassword {
		return in, newError(CodeInvalidArgument, "认证方式只能是 key / password")
	}
	if utf8.RuneCountInString(in.Remark) > maxRemarkLen {
		return in, newError(CodeInvalidArgument, "备注过长")
	}
	if len(in.Passphrase) > maxSSHPassphraseLen {
		return in, newError(CodeInvalidArgument, "私钥口令过长")
	}
	return in, nil
}

// checkSecret 校验一份私钥或密码，返回公钥指纹（密码认证为空串）。
func checkSecret(authType, secret, passphrase string) (string, error) {
	if authType == SSHAuthPassword {
		if secret == "" || len(secret) > maxSSHPasswordLen || strings.ContainsAny(secret, "\r\n") {
			return "", newError(CodeInvalidArgument, fmt.Sprintf("密码长度须为 1–%d，且不含换行", maxSSHPasswordLen))
		}
		return "", nil
	}
	if secret == "" || len(secret) > maxSSHKeyBytes {
		return "", newError(CodeInvalidArgument, "私钥不能为空，且不超过 16 KB")
	}
	fingerprint, err := remote.ParsePrivateKey([]byte(secret), []byte(passphrase))
	if err != nil {
		return "", newError(CodeInvalidArgument, "私钥无法解析，请检查格式（OpenSSH / PEM）与口令")
	}
	return fingerprint, nil
}

func credentialFromRow(r sqlcgen.GetSSHCredentialRow) SSHCredential {
	return SSHCredential{
		ID: r.ID, Name: r.Name, Username: r.Username, AuthType: r.AuthType, KeyFingerprint: r.KeyFingerprint, Remark: r.Remark,
		HostCount: int(r.HostCount), CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

// ListCredentials 返回全部 SSH 凭据。
func (s *HostService) ListCredentials(ctx context.Context) ([]SSHCredential, error) {
	rows, err := s.q.ListSSHCredentials(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ssh credentials: %w", err)
	}
	out := make([]SSHCredential, 0, len(rows))
	for _, r := range rows {
		out = append(out, credentialFromRow(sqlcgen.GetSSHCredentialRow(r)))
	}
	return out, nil
}

func (s *HostService) getCredential(ctx context.Context, id int64) (*SSHCredential, error) {
	row, err := s.q.GetSSHCredential(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgCredentialNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get ssh credential %d: %w", id, err)
	}
	c := credentialFromRow(row)
	return &c, nil
}

// sealSecret 加密并保存一份凭据的密文；passphrase 为空时口令列置空。
func (s *HostService) sealSecret(ctx context.Context, q *sqlcgen.Queries, id int64, secret, passphrase, fingerprint string) error {
	sealed, err := s.sealer.Seal([]byte(secret), secretAAD(id))
	if err != nil {
		return fmt.Errorf("seal ssh secret: %w", err)
	}
	var sealedPass []byte
	if passphrase != "" {
		if sealedPass, err = s.sealer.Seal([]byte(passphrase), passphraseAAD(id)); err != nil {
			return fmt.Errorf("seal ssh passphrase: %w", err)
		}
	}
	err = q.SetSSHCredentialSecret(ctx, sqlcgen.SetSSHCredentialSecretParams{ID: id, SecretEnc: sealed, PassphraseEnc: sealedPass, KeyFingerprint: fingerprint})
	if err != nil {
		return fmt.Errorf("save ssh secret: %w", err)
	}
	return nil
}

// CreateCredential 新建 SSH 凭据；私钥在保存前解析校验。
func (s *HostService) CreateCredential(ctx context.Context, actor *AdminPrincipal, in SSHCredentialInput, ip *netip.Addr) (*SSHCredential, error) {
	in, err := validateCredentialFields(in)
	if err != nil {
		return nil, err
	}
	fingerprint, err := checkSecret(in.AuthType, in.Secret, in.Passphrase)
	if err != nil {
		return nil, err
	}
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		id, err = q.CreateSSHCredential(ctx, sqlcgen.CreateSSHCredentialParams{Name: in.Name, Username: in.Username, AuthType: in.AuthType, Remark: in.Remark})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgCredentialNameTaken)
		}
		if err != nil {
			return fmt.Errorf("create ssh credential: %w", err)
		}
		if err := s.sealSecret(ctx, q, id, in.Secret, in.Passphrase, fingerprint); err != nil {
			return err
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionSSHCredentialCreate, TargetType: targetSSHCredential,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"name": in.Name, "username": in.Username, "authType": in.AuthType},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.getCredential(ctx, id)
}

// credentialSecret 是解密后的凭据，只在内存里短暂存在。
type credentialSecret struct {
	username   string
	authType   string
	secret     []byte
	passphrase []byte
}

func (s *HostService) openCredential(ctx context.Context, id int64) (*credentialSecret, error) {
	row, err := s.q.GetSSHCredentialSecret(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgCredentialNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get ssh credential %d: %w", id, err)
	}
	secret, err := s.sealer.Open(row.SecretEnc, secretAAD(id))
	if err != nil {
		return nil, fmt.Errorf("open ssh secret %d: %w", id, err)
	}
	cred := &credentialSecret{username: row.Username, authType: row.AuthType, secret: secret}
	if len(row.PassphraseEnc) > 0 {
		if cred.passphrase, err = s.sealer.Open(row.PassphraseEnc, passphraseAAD(id)); err != nil {
			return nil, fmt.Errorf("open ssh passphrase %d: %w", id, err)
		}
	}
	return cred, nil
}

// resolveSecret 算出修改后要保存的密文明文：提交了新的私钥 / 密码就用新的，否则沿用原值；
// 只改口令时用原私钥配新口令重新校验。isChanged 为 false 表示密文不用动。
func (s *HostService) resolveSecret(ctx context.Context, id int64, in SSHCredentialInput) (secret, passphrase, fingerprint string, isChanged bool, err error) {
	if in.Secret != "" {
		fingerprint, err = checkSecret(in.AuthType, in.Secret, in.Passphrase)
		return in.Secret, in.Passphrase, fingerprint, true, err
	}
	current, err := s.openCredential(ctx, id)
	if err != nil {
		return "", "", "", false, err
	}
	if current.authType != in.AuthType {
		return "", "", "", false, newError(CodeInvalidArgument, "改认证方式时必须重新填写私钥或密码")
	}
	if in.Passphrase == "" {
		return "", "", "", false, nil
	}
	fingerprint, err = checkSecret(in.AuthType, string(current.secret), in.Passphrase)
	return string(current.secret), in.Passphrase, fingerprint, true, err
}

// UpdateCredential 修改 SSH 凭据；Secret、Passphrase 留空沿用原值。
func (s *HostService) UpdateCredential(ctx context.Context, actor *AdminPrincipal, id int64, in SSHCredentialInput, ip *netip.Addr) (*SSHCredential, error) {
	in, err := validateCredentialFields(in)
	if err != nil {
		return nil, err
	}
	secret, passphrase, fingerprint, isSecretChanged, err := s.resolveSecret(ctx, id, in)
	if err != nil {
		return nil, err
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.UpdateSSHCredential(ctx, sqlcgen.UpdateSSHCredentialParams{ID: id, Name: in.Name, Username: in.Username, AuthType: in.AuthType, Remark: in.Remark})
		if isUniqueViolation(err) {
			return newError(CodeConflict, msgCredentialNameTaken)
		}
		if err != nil {
			return fmt.Errorf("update ssh credential %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgCredentialNotFound)
		}
		if isSecretChanged {
			if err := s.sealSecret(ctx, q, id, secret, passphrase, fingerprint); err != nil {
				return err
			}
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionSSHCredentialUpdate, TargetType: targetSSHCredential,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"name": in.Name, "username": in.Username, "authType": in.AuthType, "secretChanged": isSecretChanged},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.getCredential(ctx, id)
}

// DeleteCredential 删除 SSH 凭据；仍被主机引用时返回 409。
func (s *HostService) DeleteCredential(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	// 先取名称给审计用；“仍被引用”由外键拦（ON DELETE RESTRICT）
	cred, err := s.getCredential(ctx, id)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.DeleteSSHCredential(ctx, id)
		if isForeignKeyViolation(err) {
			return newError(CodeConflict, msgCredentialInUse)
		}
		if err != nil {
			return fmt.Errorf("delete ssh credential %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgCredentialNotFound)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionSSHCredentialDelete, TargetType: targetSSHCredential,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"name": cred.Name},
		})
	})
}

// normalizeHostAddress 校验并规范化主机地址：IP 取规范写法，域名转小写。
func normalizeHostAddress(address string, allowLoopback bool) (string, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" || len(address) > maxAddressLen {
		return "", newError(CodeInvalidArgument, msgBadHostAddress)
	}
	if ip, err := netip.ParseAddr(address); err == nil {
		ip = ip.Unmap()
		if ip.IsUnspecified() || ip.IsMulticast() || (ip.IsLoopback() && !allowLoopback) {
			return "", newError(CodeInvalidArgument, msgLoopbackAddress)
		}
		return ip.String(), nil
	}
	if !hostnamePattern.MatchString(address) {
		return "", newError(CodeInvalidArgument, msgBadHostAddress)
	}
	if address == localhostName && !allowLoopback {
		return "", newError(CodeInvalidArgument, msgLoopbackAddress)
	}
	return address, nil
}

func validatePort(port int) error {
	if port < minPort || port > maxPort {
		return newError(CodeInvalidArgument, msgBadPort)
	}
	return nil
}

func int4OrNull(v *int) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true} //nolint:gosec // 端口已校验在 1–65535
}
