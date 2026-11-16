package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/remote"
)

// 主机任务的种类、状态与步骤，取值与 docs/03 §5.22、hub-api 的 HostTaskKind / HostTaskStatus 逐项对应。
const (
	HostTaskProvision    = "provision"
	HostTaskVerify       = "verify"
	HostTaskRestartPanel = "restart_panel"
	HostTaskRestartCore  = "restart_core"
	HostTaskUpgrade      = "upgrade"
	HostTaskReissueToken = "reissue_token"
	HostTaskEnableTLS    = "enable_tls"
	HostTaskDiagnose     = "diagnose"

	HostTaskQueued    = "queued"
	HostTaskRunning   = "running"
	HostTaskSucceeded = "succeeded"
	HostTaskFailed    = "failed"
	HostTaskCanceled  = "canceled"

	hostStepConnect  = "connect"
	hostStepDetect   = "detect"
	hostStepInstall  = "install"
	hostStepAdopt    = "adopt"
	hostStepRegister = "register"
	hostStepRun      = "run"
)

const (
	maxHostTaskErrorLen = 500
	hostTaskListLimit   = 20
	// taskWriteTimeout 是任务收尾时写库的时限；此时任务自己的 ctx 可能已超时，所以另起一个。
	taskWriteTimeout = 10 * time.Second
	schemeHTTP       = "http"
	schemeHTTPS      = "https"
	logTimeLayout    = "15:04:05"

	msgTaskNotFound      = "任务不存在"
	msgTaskNotQueued     = "任务已开始或已结束，不能取消"
	msgNeedsNoServer     = "这台主机已接入服务器，不需要再部署"
	msgNeedsServer       = "这台主机还没接入服务器，先部署"
	msgBadAction         = "不支持的动作"
	msgInterrupted       = "平台重启，任务中断"
	msgTaskTimeout       = "任务超时"
	msgTaskInternal      = "内部错误"
	msgNoDeployer        = "这类面板不支持经 SSH 部署"
	msgNoStandardPanel   = "主机上没有标准安装的面板"
	msgPanelUnreachable  = "面板端口从平台连不上，请检查防火墙或云厂商安全组后重做"
	msgPanelRejectsToken = "面板拒绝了新签发的令牌"
	msgAlreadyHTTPS      = "面板已经是 HTTPS"
	msgLoginBusy         = "正在重置这台主机的面板登录口令"
)

// actionKinds 是管理员可以直接发起的动作；verify 只由绑定服务器时产生。
var actionKinds = map[string]struct{}{
	HostTaskProvision: {}, HostTaskRestartPanel: {}, HostTaskRestartCore: {}, HostTaskUpgrade: {},
	HostTaskReissueToken: {}, HostTaskEnableTLS: {}, HostTaskDiagnose: {},
}

// reasonMessages 是检测阶段各原因的说明（docs/03 §5.22 “不能部署的原因”）。
var reasonMessages = map[deploy.Reason]string{
	deploy.ReasonUnsupportedOS: "不是受支持的发行版",
	deploy.ReasonNoSystemd:     "主机没有 systemd",
	deploy.ReasonNoRoot:        "登录用户不是 root，也不能免密 sudo",
	deploy.ReasonNonstandard:   "主机上的面板不是标准安装（如 Docker），请改用手动接入",
	deploy.ReasonVersionTooOld: "已有面板的版本过低，请先升级到 v3.0 以上",
}

// HostTask 是一条主机任务（不含日志）。
type HostTask struct {
	ID            int64
	HostID        int64
	Kind          string
	Status        string
	Step          string
	ErrorCode     string
	LastError     string
	CreatedByName string
	CreatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
}

// HostTaskDetail 是带日志的任务。
type HostTaskDetail struct {
	HostTask
	Log string
}

func taskFromListRow(r sqlcgen.ListHostTasksRow) HostTask {
	return HostTask{
		ID: r.ID, HostID: r.HostID, Kind: r.Kind, Status: r.Status, Step: r.Step, ErrorCode: r.ErrorCode, LastError: r.LastError,
		CreatedByName: r.CreatedByName, CreatedAt: r.CreatedAt.Time, StartedAt: timePtr(r.StartedAt), FinishedAt: timePtr(r.FinishedAt),
	}
}

// ListTasks 返回一台主机最近的任务，新的在前；limit ≤ 0 时取默认条数。
func (s *HostService) ListTasks(ctx context.Context, hostID int64, limit int) ([]HostTask, error) {
	if limit <= 0 {
		limit = hostTaskListLimit
	}
	rows, err := s.q.ListHostTasks(ctx, sqlcgen.ListHostTasksParams{HostID: hostID, Limit: int32(limit)}) //nolint:gosec // limit 是包内常量级的小数
	if err != nil {
		return nil, fmt.Errorf("list tasks of host %d: %w", hostID, err)
	}
	out := make([]HostTask, 0, len(rows))
	for _, r := range rows {
		out = append(out, taskFromListRow(r))
	}
	return out, nil
}

// ListHostTasks 返回一台主机最近 20 条任务；主机不存在返回 404。
func (s *HostService) ListHostTasks(ctx context.Context, hostID int64) ([]HostTask, error) {
	if _, err := s.hostRow(ctx, hostID); err != nil {
		return nil, err
	}
	return s.ListTasks(ctx, hostID, hostTaskListLimit)
}

// GetTask 返回一条任务及其日志。
func (s *HostService) GetTask(ctx context.Context, id int64) (*HostTaskDetail, error) {
	r, err := s.q.GetHostTask(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgTaskNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get host task %d: %w", id, err)
	}
	return &HostTaskDetail{Log: r.Log, HostTask: HostTask{
		ID: r.ID, HostID: r.HostID, Kind: r.Kind, Status: r.Status, Step: r.Step, ErrorCode: r.ErrorCode, LastError: r.LastError,
		CreatedByName: r.CreatedByName, CreatedAt: r.CreatedAt.Time, StartedAt: timePtr(r.StartedAt), FinishedAt: timePtr(r.FinishedAt),
	}}, nil
}

// insertTask 在事务里排一条任务并写审计；该主机已有未结束的任务时返回 409（由部分唯一索引保证）。
func (s *HostService) insertTask(ctx context.Context, q *sqlcgen.Queries, actor *AdminPrincipal, ip *netip.Addr, hostID int64, kind string) (int64, error) {
	id, err := q.CreateHostTask(ctx, sqlcgen.CreateHostTaskParams{HostID: hostID, Kind: kind, CreatedBy: pgtype.Int8{Int64: actor.AdminID, Valid: actor.AdminID != 0}})
	if isUniqueViolation(err) {
		return 0, newError(CodeConflict, msgHostBusy)
	}
	if err != nil {
		return 0, fmt.Errorf("create host task: %w", err)
	}
	return id, writeAudit(ctx, q, AuditEntry{
		ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionHostTaskCreate, TargetType: targetHostTask, TargetID: fmt.Sprint(id), ClientIP: ip,
		Detail: map[string]any{"hostId": hostID, "kind": kind},
	})
}

// CreateAction 对主机发起一个动作，排成一条任务。provision 只在还没接入服务器时可用，
// 其余动作（diagnose 除外）只在已接入时可用；已有未结束的任务返回 409。按管理员频控。
func (s *HostService) CreateAction(ctx context.Context, actor *AdminPrincipal, hostID int64, kind string, ip *netip.Addr) (*HostTask, error) {
	if _, ok := actionKinds[kind]; !ok {
		return nil, newError(CodeInvalidArgument, msgBadAction)
	}
	host, err := s.hostRow(ctx, hostID)
	if err != nil {
		return nil, err
	}
	switch {
	case kind == HostTaskProvision && host.ServerID.Valid:
		return nil, newError(CodeConflict, msgNeedsNoServer)
	case kind != HostTaskProvision && kind != HostTaskDiagnose && !host.ServerID.Valid:
		return nil, newError(CodeConflict, msgNeedsServer)
	}
	if err := allow(s.actions, actor.AdminID); err != nil {
		return nil, err
	}
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		id, err = s.insertTask(ctx, q, actor, ip, hostID, kind)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.kickRunner()
	task, err := s.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	return &task.HostTask, nil
}

// CancelTask 取消一条还在排队的任务；已开始或已结束的返回 409。
func (s *HostService) CancelTask(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) (*HostTask, error) {
	if _, err := s.GetTask(ctx, id); err != nil {
		return nil, err
	}
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.CancelHostTask(ctx, id)
		if err != nil {
			return fmt.Errorf("cancel host task %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeConflict, msgTaskNotQueued)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionHostTaskCancel, TargetType: targetHostTask, TargetID: fmt.Sprint(id), ClientIP: ip,
		})
	})
	if err != nil {
		return nil, err
	}
	task, err := s.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	return &task.HostTask, nil
}

// ClaimTasks 领取至多 limit 条排队的任务并置为进行中，返回它们的 id。
func (s *HostService) ClaimTasks(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 {
		return nil, nil
	}
	ids, err := s.q.ClaimQueuedHostTasks(ctx, int32(limit)) //nolint:gosec // limit 是并发上限，很小
	if err != nil {
		return nil, fmt.Errorf("claim host tasks: %w", err)
	}
	return ids, nil
}

// FailInterrupted 把上次进程退出时没跑完的任务记为失败（Ruling-71），返回条数；启动时调一次。
func (s *HostService) FailInterrupted(ctx context.Context) (int64, error) {
	n, err := s.q.FailRunningHostTasks(ctx, sqlcgen.FailRunningHostTasksParams{ErrorCode: string(deploy.ReasonInterrupted), LastError: msgInterrupted})
	if err != nil {
		return 0, fmt.Errorf("fail interrupted host tasks: %w", err)
	}
	return n, nil
}

// taskRun 是一条任务执行期间的上下文。
type taskRun struct {
	svc     *HostService
	id      int64
	kind    string
	adminID int64
	host    sqlcgen.GetServerHostRow
	// secrets 是执行中拿到的密文明文，写日志前一律替换掉。
	secrets []string
}

// logf 往任务日志追加一行；先脱敏。日志写失败只记本地日志，不影响任务本身。
func (t *taskRun) logf(format string, args ...any) {
	text := strings.TrimRight(deploy.Redact(fmt.Sprintf(format, args...), t.secrets...), "\n")
	if text == "" {
		return
	}
	line := "[" + t.svc.now().UTC().Format(logTimeLayout) + "] " + text + "\n"
	ctx, cancel := context.WithTimeout(context.Background(), taskWriteTimeout)
	defer cancel()
	if err := t.svc.q.AppendHostTaskLog(ctx, sqlcgen.AppendHostTaskLogParams{ID: t.id, Line: line}); err != nil {
		slog.Warn("append host task log", "task_id", t.id, "err", err)
	}
}

func (t *taskRun) step(ctx context.Context, step, note string) {
	if err := t.svc.q.SetHostTaskStep(ctx, sqlcgen.SetHostTaskStepParams{ID: t.id, Step: step}); err != nil {
		slog.Warn("set host task step", "task_id", t.id, "err", err)
	}
	t.logf("%s", note)
}

// RunTask 执行一条已领取的任务并记录结果；不返回错误，失败都落在任务上。
func (s *HostService) RunTask(ctx context.Context, taskID int64) {
	err := s.runTask(ctx, taskID)
	status, code, message := HostTaskSucceeded, "", ""
	if err != nil {
		reason, msg := taskFailure(err)
		status, code, message = HostTaskFailed, string(reason), truncate(deploy.Redact(msg), maxHostTaskErrorLen)
		slog.Warn("host task failed", "task_id", taskID, "reason", reason, "err", err)
	}
	wctx, cancel := context.WithTimeout(context.Background(), taskWriteTimeout)
	defer cancel()
	if _, ferr := s.q.FinishHostTask(wctx, sqlcgen.FinishHostTaskParams{ID: taskID, Status: status, ErrorCode: code, LastError: message}); ferr != nil {
		slog.Error("finish host task", "task_id", taskID, "err", ferr)
	}
}

// taskFailure 把执行中的错误归到 docs/03 §5.22 的原因大类，并给出可展示的摘要。
func taskFailure(err error) (deploy.Reason, string) {
	var se *Error
	switch {
	case deploy.AsError(err) != nil:
		de := deploy.AsError(err)
		return de.Reason, de.Message
	case remote.AsError(err) != nil:
		re := remote.AsError(err)
		if errors.Is(re.Err, context.DeadlineExceeded) {
			return deploy.ReasonCommandFailed, msgTaskTimeout
		}
		return deploy.Reason(re.Kind), re.Message
	case errors.Is(err, context.DeadlineExceeded):
		return deploy.ReasonCommandFailed, msgTaskTimeout
	case errors.As(err, &se):
		return deploy.ReasonCommandFailed, se.Message
	default:
		return deploy.ReasonCommandFailed, msgTaskInternal
	}
}

func (s *HostService) taskTimeout(kind string) time.Duration {
	if kind == HostTaskProvision || kind == HostTaskUpgrade {
		return s.cfg.InstallTimeout
	}
	return s.cfg.CommandTimeout
}

func (s *HostService) runTask(ctx context.Context, taskID int64) error {
	task, err := s.q.GetHostTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("load host task %d: %w", taskID, err)
	}
	host, err := s.hostRow(ctx, task.HostID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.taskTimeout(task.Kind))
	defer cancel()
	t := &taskRun{svc: s, id: taskID, kind: task.Kind, adminID: task.CreatedBy.Int64, host: host}
	deployer, ok := s.deployers.Get(host.ProviderType)
	if !ok {
		return deploy.NewError(deploy.ReasonCommandFailed, msgNoDeployer)
	}

	t.step(ctx, hostStepConnect, fmt.Sprintf("连接 %s", net.JoinHostPort(host.Address, strconv.Itoa(int(host.SshPort)))))
	target, err := s.dialTarget(ctx, host.Address, int(host.SshPort), host.CredentialID, host.HostKeySha256)
	if err != nil {
		return err
	}
	runner, err := s.dialer.Dial(ctx, target)
	if err != nil {
		return err
	}
	defer closeRunner(runner)
	if host.HostKeySha256 == "" {
		// 首次连接：记录并固定主机密钥（Ruling-68）
		if err := s.q.SetServerHostKey(ctx, sqlcgen.SetServerHostKeyParams{ID: host.ID, HostKeySha256: runner.HostKeySHA256()}); err != nil {
			return fmt.Errorf("pin host key of %d: %w", host.ID, err)
		}
		t.logf("已记录主机密钥 %s", runner.HostKeySHA256())
	}

	t.step(ctx, hostStepDetect, "检测系统与面板")
	det, err := deployer.Detect(ctx, runner)
	if err != nil {
		return err
	}
	if err := s.q.SetServerHostDetection(ctx, sqlcgen.SetServerHostDetectionParams{ID: host.ID, OsID: det.OSID, Arch: det.Arch}); err != nil {
		return fmt.Errorf("save detection of host %d: %w", host.ID, err)
	}
	t.logf("系统 %s / %s，面板：%s %s", det.OSID, det.Arch, det.PanelState, det.PanelVersion)

	switch task.Kind {
	case HostTaskProvision:
		return t.provision(ctx, deployer, runner, det)
	case HostTaskDiagnose:
		return t.diagnose(ctx, deployer, runner)
	default:
		return t.operate(ctx, deployer, runner, det)
	}
}

// requireStandardPanel 要求主机可提权且有标准安装的面板；运维动作与校验连接都以此为前提。
func requireStandardPanel(det deploy.Detection) error {
	if !det.IsPrivileged {
		return deploy.NewError(deploy.ReasonNoRoot, reasonMessages[deploy.ReasonNoRoot])
	}
	if det.PanelState != deploy.PanelStandard {
		return deploy.NewError(deploy.ReasonNonstandard, msgNoStandardPanel)
	}
	return nil
}

func (t *taskRun) diagnose(ctx context.Context, d deploy.Deployer, r remote.Runner) error {
	t.step(ctx, hostStepRun, "读取服务状态与最近日志")
	text, err := d.Diagnose(ctx, r)
	if err != nil {
		return err
	}
	t.logf("%s", text)
	return nil
}

// provision 是部署流程 ②–④（docs/03 §5.22）：没有面板就安装并配证书，有标准安装的面板就接管，然后接入平台。
func (t *taskRun) provision(ctx context.Context, d deploy.Deployer, r remote.Runner, det deploy.Detection) error {
	if det.Reason != deploy.ReasonNone {
		return deploy.NewError(det.Reason, reasonMessages[det.Reason])
	}
	isFresh := det.Plan == deploy.PlanInstall
	if isFresh {
		t.step(ctx, hostStepInstall, "主机上没有面板，开始安装（平台基线版本）")
		opts := deploy.InstallOptions{}
		if t.host.PanelPort.Valid {
			opts.PanelPort = int(t.host.PanelPort.Int32)
		}
		if err := d.Install(ctx, r, opts, func(line string) { t.logf("%s", line) }); err != nil {
			return err
		}
		t.logf("安装完成，生成自签证书并改为 HTTPS")
		if _, err := d.EnableTLS(ctx, r, t.host.Address); err != nil {
			return err
		}
	} else {
		t.step(ctx, hostStepAdopt, "主机上已有面板，读取现有配置并签发令牌")
	}
	access, err := d.Access(ctx, r)
	if err != nil {
		return err
	}
	if isFresh {
		if err := d.OpenFirewall(ctx, r, access.Port); err != nil {
			return err
		}
	}
	token, err := d.IssueToken(ctx, r)
	if err != nil {
		return err
	}
	t.secrets = append(t.secrets, token)

	t.step(ctx, hostStepRegister, fmt.Sprintf("面板端口 %d，接入平台", access.Port))
	conn, err := t.chooseConn(ctx, access, token, isFresh)
	if err != nil {
		return err
	}
	serverID, err := t.svc.panels.AttachPanel(ctx, t.adminID, ServerInput{ServerConnInput: conn, Name: t.host.Name, Region: t.host.Region})
	if err != nil {
		if errors.Is(err, ErrConflict) {
			return deploy.NewError(deploy.ReasonNameTaken, "服务器名称已被占用，请改主机名称后重做")
		}
		return attachError(err)
	}
	err = t.svc.q.SetServerHostServer(ctx, sqlcgen.SetServerHostServerParams{ID: t.host.ID, ServerID: pgtype.Int8{Int64: serverID, Valid: true}})
	if isUniqueViolation(err) {
		return deploy.NewError(deploy.ReasonCommandFailed, "这台面板对应的服务器已关联到另一台主机")
	}
	if err != nil {
		return fmt.Errorf("link host %d to server %d: %w", t.host.ID, serverID, err)
	}
	if conn.TLSMode == panel.TLSVerify && !access.HasTLS {
		t.logf("注意：面板是明文 HTTP，令牌以明文传输；可在服务器详情页的 SSH 运维里“配置证书”")
	}
	t.logf("已接入，服务器 id %d", serverID)
	return nil
}

// buildBaseURL 用主机地址与读出的端口、路径拼面板根地址；根路径不带结尾的斜杠。
func buildBaseURL(isTLS bool, address string, port int, basePath string) string {
	scheme := schemeHTTP
	if isTLS {
		scheme = schemeHTTPS
	}
	return scheme + "://" + net.JoinHostPort(address, strconv.Itoa(port)) + strings.TrimRight(basePath, "/")
}

// chooseConn 决定用什么方式接入面板并等它可达（Ruling-70）：没有证书走 http；
// 新装的面板是平台刚配的自签证书，直接 pin；接管的面板先按 verify 试一次，不通过再 pin。
func (t *taskRun) chooseConn(ctx context.Context, access deploy.PanelAccess, token string, isFresh bool) (ServerConnInput, error) {
	conn := ServerConnInput{
		ProviderType: t.host.ProviderType, APIToken: token, TLSMode: panel.TLSVerify,
		BaseURL: buildBaseURL(access.HasTLS, t.host.Address, access.Port, access.BasePath),
	}
	if access.HasTLS && !isFresh {
		if _, err := t.svc.panels.probeConn(ctx, conn); err == nil {
			return conn, nil
		}
		t.logf("证书未通过系统 CA 校验，改为固定证书指纹")
	}
	if access.HasTLS {
		conn.TLSMode, conn.CertPinSHA256 = panel.TLSPin, access.CertSHA256
	}
	err := t.svc.waitFor(ctx, func() error {
		_, err := t.svc.panels.probeConn(ctx, conn)
		return err
	})
	if err != nil {
		return conn, attachError(err)
	}
	return conn, nil
}

// attachError 把接入阶段的面板错误归类：连不上多半是防火墙，令牌被拒是面板侧的问题。
func attachError(err error) error {
	if pe := panel.AsError(err); pe != nil {
		if pe.Kind == panel.KindAuth {
			return deploy.NewError(deploy.ReasonCommandFailed, msgPanelRejectsToken)
		}
		return deploy.NewError(deploy.ReasonPanelUnreachable, msgPanelUnreachable)
	}
	if errors.Is(err, ErrUpstream) {
		return deploy.NewError(deploy.ReasonPanelUnreachable, msgPanelUnreachable)
	}
	return err
}

// waitFor 反复调用 probe 直到成功或次数用尽；面板重启后要几秒才回来。
func (s *HostService) waitFor(ctx context.Context, probe func() error) error {
	var err error
	for attempt := range s.cfg.PanelWaitAttempts {
		if attempt > 0 {
			timer := time.NewTimer(s.cfg.PanelWaitInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err = probe(); err == nil {
			return nil
		}
	}
	return err
}

// operate 执行对已接入主机的运维动作与校验连接；期间占用该服务器的对账锁（docs/03 §5.22）。
func (t *taskRun) operate(ctx context.Context, d deploy.Deployer, r remote.Runner, det deploy.Detection) error {
	if err := requireStandardPanel(det); err != nil {
		return err
	}
	if t.kind == HostTaskVerify {
		t.logf("连接正常，检测到标准安装的面板")
		return nil
	}
	if !t.host.ServerID.Valid {
		return deploy.NewError(deploy.ReasonCommandFailed, msgNeedsServer)
	}
	serverID := t.host.ServerID.Int64
	unlock := lockServer(serverID)
	defer unlock()
	switch t.kind {
	case HostTaskRestartPanel:
		t.step(ctx, hostStepRun, "重启面板")
		if err := d.RestartPanel(ctx, r); err != nil {
			return err
		}
	case HostTaskRestartCore:
		t.step(ctx, hostStepRun, "让面板重载内核")
		if err := d.RestartCore(ctx, r); err != nil {
			return err
		}
	case HostTaskUpgrade:
		t.step(ctx, hostStepRun, "升级 / 修复到平台基线版本")
		if err := d.Upgrade(ctx, r, func(line string) { t.logf("%s", line) }); err != nil {
			return err
		}
	case HostTaskReissueToken:
		return t.reissueToken(ctx, d, r, serverID)
	case HostTaskEnableTLS:
		return t.enableTLS(ctx, d, r, serverID)
	default:
		return deploy.NewError(deploy.ReasonCommandFailed, msgBadAction)
	}
	return t.confirmOnline(ctx, serverID)
}

// confirmOnline 在动作之后探测面板，确认它回到在线。
func (t *taskRun) confirmOnline(ctx context.Context, serverID int64) error {
	if err := t.svc.waitFor(ctx, func() error { return t.svc.panels.ProbeServer(ctx, serverID) }); err != nil {
		return deploy.NewError(deploy.ReasonPanelUnreachable, "动作已执行，但面板没有回到在线")
	}
	t.logf("面板在线")
	return nil
}

func (t *taskRun) reissueToken(ctx context.Context, d deploy.Deployer, r remote.Runner, serverID int64) error {
	t.step(ctx, hostStepRun, "重新签发平台令牌")
	token, err := d.IssueToken(ctx, r)
	if err != nil {
		return err
	}
	t.secrets = append(t.secrets, token)
	current, err := t.svc.q.GetServerConn(ctx, serverID)
	if err != nil {
		return fmt.Errorf("get server %d: %w", serverID, err)
	}
	conn := ServerConnInput{
		ProviderType: current.ProviderType, BaseURL: current.BaseUrl, APIToken: token,
		TLSMode: current.TlsMode, CertPinSHA256: current.CertPinSha256.String,
	}
	if err := t.svc.panels.ReplaceServerConn(ctx, t.adminID, serverID, conn); err != nil {
		return attachError(err)
	}
	t.logf("令牌已更新，面板在线")
	return nil
}

func (t *taskRun) enableTLS(ctx context.Context, d deploy.Deployer, r remote.Runner, serverID int64) error {
	current, err := t.svc.q.GetServerConn(ctx, serverID)
	if err != nil {
		return fmt.Errorf("get server %d: %w", serverID, err)
	}
	u, err := panelHost(current.BaseUrl)
	if err != nil {
		return err
	}
	if u.Scheme != schemeHTTP {
		return deploy.NewError(deploy.ReasonCommandFailed, msgAlreadyHTTPS)
	}
	t.step(ctx, hostStepRun, "生成自签证书并设给面板")
	pin, err := d.EnableTLS(ctx, r, u.Hostname())
	if err != nil {
		return err
	}
	u.Scheme = schemeHTTPS
	conn := ServerConnInput{ProviderType: current.ProviderType, BaseURL: u.String(), TLSMode: panel.TLSPin, CertPinSHA256: pin}
	err = t.svc.waitFor(ctx, func() error { return t.svc.panels.ReplaceServerConn(ctx, t.adminID, serverID, conn) })
	if err != nil {
		return attachError(err)
	}
	t.logf("面板已改为 HTTPS，平台按证书指纹接入")
	return nil
}

// ResetPanelLogin 重置面板网页的登录用户名与口令，结果只返回这一次（docs/03 §5.22）。
// 只对已接入服务器的主机可用；有未结束的任务时返回 409；SSH 或命令失败返回 502。
func (s *HostService) ResetPanelLogin(ctx context.Context, actor *AdminPrincipal, hostID int64, ip *netip.Addr) (*deploy.PanelLogin, error) {
	host, err := s.hostRow(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if !host.ServerID.Valid {
		return nil, newError(CodeConflict, msgNeedsServer)
	}
	if err := s.ensureIdle(ctx, s.q, hostID); err != nil {
		return nil, err
	}
	deployer, ok := s.deployers.Get(host.ProviderType)
	if !ok {
		return nil, newError(CodeConflict, msgNoDeployer)
	}
	if err := allow(s.actions, actor.AdminID); err != nil {
		return nil, err
	}
	if !s.logins.tryLock(hostID) {
		return nil, newError(CodeConflict, msgLoginBusy)
	}
	defer s.logins.unlock(hostID)
	target, err := s.dialTarget(ctx, host.Address, int(host.SshPort), host.CredentialID, host.HostKeySha256)
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
	login, err := deployer.ResetPanelLogin(ctx, runner)
	if err != nil {
		return nil, upstreamError(err)
	}
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionHostPanelLoginReset, TargetType: targetHost, TargetID: fmt.Sprint(hostID), ClientIP: ip,
			Detail: map[string]any{"serverId": host.ServerID.Int64},
		})
	})
	if err != nil {
		return nil, err
	}
	return &login, nil
}

// closeRunner 关闭用完的 SSH 连接；此时动作的结果已定，关闭失败只记日志。
func closeRunner(r remote.Runner) {
	if err := r.Close(); err != nil {
		slog.Debug("close ssh runner", "err", err)
	}
}
