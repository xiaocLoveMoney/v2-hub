package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/inboundtpl"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/remote"
)

// 一键部署入站（docs/03 §5.1 “一键部署入站”，Ruling-76）。
const (
	HostTaskDeployInbounds = "deploy_inbounds"

	hostStepPorts      = "ports"
	hostStepInbounds   = "inbounds"
	hostStepFirewall   = "firewall"
	hostStepXray       = "xray"
	hostStepNodeGroups = "node_groups"

	// presetPortMin / presetPortMax 是建议端口的范围。
	presetPortMin = 10000
	presetPortMax = 60000
	// maxPresetNodeGroups 与 hub-api InboundPresetDeployInput.nodeGroupIds 的上限一致。
	maxPresetNodeGroups = 50
	// coreRunningStreak 是认定 Xray 稳定在跑要连续看到 running 的次数：端口冲突时 Xray 每隔几秒重启一次，偶尔也会报 running。
	coreRunningStreak  = 3
	defaultRealityPort = 443

	msgServerNoHost        = "这台服务器还没绑定 SSH，先在 SSH 运维里绑定"
	msgPresetNoDNS         = "主机没有 DNS 账号，申请不了证书"
	msgPresetUnknown       = "不认识的预设："
	msgPresetDuplicate     = "同一个预设只能选一次"
	msgPresetPortDuplicate = "端口不能重复"
	msgPresetPortRange     = "端口须在 1–65535 之间"
	msgPresetNoItems       = "至少选一个预设"
	msgPresetTooManyGroups = "节点组最多选 50 个"
	msgPresetGroupMissing  = "节点组不存在"
	msgPortsInUse          = "这些端口已被主机上的程序或面板已有入站占用："
	msgNoRealityTarget     = "面板的候选站点里没有可用的 REALITY 目标"
	msgNoMLKEM             = "面板没有返回 ML-KEM-768 的 VLESS Encryption 参数"
	msgNoPanelCert         = "面板上没有证书文件"
	msgXrayDown            = "入站建好了，但面板的 Xray 没在运行"
	msgPresetsFailed       = "%d 项里有 %d 项没建成：%s"
	vlessEncAuthMLKEM      = "mlkem768"
)

// reservedPresetPorts 不作建议端口：平台与常见网站占着（与平台同机时 Caddy 就在 80 / 443）。
var reservedPresetPorts = []int{80, 443}

// InboundPresetItem 是要部署的一个预设与它的端口。
type InboundPresetItem struct {
	Key  string `json:"key"`
	Port int    `json:"port"`
}

// InboundPresetDeployInput 是一键部署的参数，原样存进任务的 payload。
type InboundPresetDeployInput struct {
	Items        []InboundPresetItem `json:"items"`
	NodeGroupIDs []int64             `json:"nodeGroupIds"`
}

// InboundPresetOption 是预览里的一项。
type InboundPresetOption struct {
	Key       string
	Available bool
	Reason    string
	Port      int
	NeedsCert bool
}

// InboundPresetPreview 是一键部署的预览。
type InboundPresetPreview struct {
	HostID    int64
	Domain    string
	CertReady bool
	Items     []InboundPresetOption
}

// hostOfServer 取服务器关联的主机；没绑 SSH 返回 409。
func (s *HostService) hostOfServer(ctx context.Context, serverID int64) (sqlcgen.GetServerHostRow, error) {
	if _, err := s.panels.GetServer(ctx, serverID); err != nil {
		return sqlcgen.GetServerHostRow{}, err
	}
	hostID, err := s.q.GetServerHostIDByServer(ctx, pgtype.Int8{Int64: serverID, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.GetServerHostRow{}, newError(CodeConflict, msgServerNoHost)
	}
	if err != nil {
		return sqlcgen.GetServerHostRow{}, fmt.Errorf("host of server %d: %w", serverID, err)
	}
	return s.hostRow(ctx, hostID)
}

// presetUnavailable 给出预设在这台主机上不能部署的原因；能部署为空串。
func presetUnavailable(key string, host sqlcgen.GetServerHostRow) string {
	if inboundtpl.PresetNeedsCert(key) && (!host.DnsAccountID.Valid || host.Domain == "") {
		return msgPresetNoDNS
	}
	return ""
}

// PreviewInboundPresets 给出 7 个预设能否部署与建议端口：经 SSH 读一次主机正在监听的端口，避开它们与面板已有入站。
// 服务器没绑 SSH 返回 409；按管理员频控（与连接测试共用额度）；SSH 失败返回 502。
func (s *HostService) PreviewInboundPresets(ctx context.Context, actor *AdminPrincipal, serverID int64) (*InboundPresetPreview, error) {
	host, err := s.hostOfServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	deployer, ok := s.deployers.Get(host.ProviderType)
	if !ok {
		return nil, newError(CodeConflict, msgNoDeployer)
	}
	if err := allow(s.tests, actor.AdminID); err != nil {
		return nil, err
	}
	var listening []deploy.Port
	target := endpoint{address: host.Address, port: int(host.SshPort), credentialID: host.CredentialID, pinnedKey: host.HostKeySha256}
	err = s.withRunner(ctx, target, func(ctx context.Context, r remote.Runner) error {
		var err error
		listening, err = deployer.ListeningPorts(ctx, r)
		return err
	})
	if err != nil {
		return nil, err
	}
	used, err := s.usedPorts(ctx, serverID, listening)
	if err != nil {
		return nil, err
	}
	out := &InboundPresetPreview{HostID: host.ID, Domain: host.Domain, CertReady: host.CertNotAfter.Valid, Items: make([]InboundPresetOption, 0, len(inboundtpl.PresetKeys))}
	taken := make(map[int]bool, len(inboundtpl.PresetKeys))
	for _, key := range inboundtpl.PresetKeys {
		port, err := pickPresetPort(used, taken)
		if err != nil {
			return nil, err
		}
		taken[port] = true
		reason := presetUnavailable(key, host)
		out.Items = append(out.Items, InboundPresetOption{Key: key, Available: reason == "", Reason: reason, Port: port, NeedsCert: inboundtpl.PresetNeedsCert(key)})
	}
	return out, nil
}

// portUse 记一个端口的 TCP / UDP 是否被占；面板已有入站的端口两种都算占用。
type portUse map[int]struct{ tcp, udp bool }

// usedPorts 合并主机正在监听的端口与面板上已有入站的端口。
func (s *HostService) usedPorts(ctx context.Context, serverID int64, listening []deploy.Port) (portUse, error) {
	inbounds, err := s.panels.ListServerInbounds(ctx, serverID)
	if err != nil {
		return nil, err
	}
	used := make(portUse, len(listening)+len(inbounds))
	for _, p := range listening {
		u := used[p.Number]
		if p.UDP {
			u.udp = true
		} else {
			u.tcp = true
		}
		used[p.Number] = u
	}
	for _, in := range inbounds {
		used[in.Port] = struct{ tcp, udp bool }{true, true}
	}
	return used, nil
}

// conflicts 报告预设的端口是否与已占用的冲突（按它监听的协议）。
func (u portUse) conflicts(key string, port int) bool {
	tcp, udp := inboundtpl.PresetPorts(key)
	cur := u[port]
	return (tcp && cur.tcp) || (udp && cur.udp)
}

// pickPresetPort 在建议范围里随机取一个两种协议都没被占、也没被这次别的预设取走的端口。
func pickPresetPort(used portUse, taken map[int]bool) (int, error) {
	span := big.NewInt(presetPortMax - presetPortMin + 1)
	for range presetPortMax - presetPortMin {
		n, err := rand.Int(rand.Reader, span)
		if err != nil {
			return 0, fmt.Errorf("pick preset port: %w", err)
		}
		port := presetPortMin + int(n.Int64())
		cur := used[port]
		if cur.tcp || cur.udp || taken[port] || slices.Contains(reservedPresetPorts, port) {
			continue
		}
		return port, nil
	}
	return 0, errors.New("pick preset port: range exhausted")
}

// validatePresetInput 校验预设、端口与节点组；不可用的预设（主机没有 DNS 账号时的 TLS 预设）返回 400。
func (s *HostService) validatePresetInput(ctx context.Context, host sqlcgen.GetServerHostRow, in InboundPresetDeployInput) error {
	if len(in.Items) == 0 || len(in.Items) > len(inboundtpl.PresetKeys) {
		return newError(CodeInvalidArgument, msgPresetNoItems)
	}
	if len(in.NodeGroupIDs) > maxPresetNodeGroups {
		return newError(CodeInvalidArgument, msgPresetTooManyGroups)
	}
	keys := make(map[string]bool, len(in.Items))
	ports := make(map[int]bool, len(in.Items))
	for _, it := range in.Items {
		switch {
		case !inboundtpl.IsPreset(it.Key):
			return newError(CodeInvalidArgument, msgPresetUnknown+it.Key)
		case keys[it.Key]:
			return newError(CodeInvalidArgument, msgPresetDuplicate)
		case it.Port < minPresetPort || it.Port > maxPresetPort:
			return newError(CodeInvalidArgument, msgPresetPortRange)
		case ports[it.Port]:
			return newError(CodeInvalidArgument, msgPresetPortDuplicate)
		}
		if reason := presetUnavailable(it.Key, host); reason != "" {
			return newError(CodeInvalidArgument, reason)
		}
		keys[it.Key], ports[it.Port] = true, true
	}
	if len(in.NodeGroupIDs) == 0 {
		return nil
	}
	groups, err := s.panels.ListNodeGroups(ctx)
	if err != nil {
		return err
	}
	for _, id := range in.NodeGroupIDs {
		if !slices.ContainsFunc(groups, func(g NodeGroup) bool { return g.ID == id }) {
			return newError(CodeInvalidArgument, msgPresetGroupMissing)
		}
	}
	return nil
}

const (
	minPresetPort = 1
	maxPresetPort = 65535
)

// DeployInboundPresets 排一条 deploy_inbounds 主机任务；服务器没绑 SSH、主机有未结束的任务返回 409。按管理员频控。
func (s *HostService) DeployInboundPresets(ctx context.Context, actor *AdminPrincipal, serverID int64, in InboundPresetDeployInput, ip *netip.Addr) (*HostTask, error) {
	host, err := s.hostOfServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	in.NodeGroupIDs = slices.Compact(slices.Sorted(slices.Values(in.NodeGroupIDs)))
	if err := s.validatePresetInput(ctx, host, in); err != nil {
		return nil, err
	}
	if err := allow(s.actions, actor.AdminID); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("encode preset payload: %w", err)
	}
	var id int64
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		id, err = s.insertTaskWithPayload(ctx, q, adminOrigin(actor, ip), host.ID, HostTaskDeployInbounds, payload)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.kickRunner()
	return s.task(ctx, id)
}

// presetResult 是一项预设的结果。
type presetResult struct {
	item      InboundPresetItem
	remark    string
	inboundID int64
	nodeID    int64
	err       error
}

// presetEnv 是建入站前准备好的公共参数；各自的 err 只让用到它的预设失败。
type presetEnv struct {
	cert    panel.CertFiles
	certErr error
	target  string
	realErr error
	dec     string
	enc     string
	encErr  error
}

// deployInbounds 执行一键部署（docs/03 §5.1）：查端口 → 需要时配证书 → 准备 REALITY 目标与 VLESS Encryption 参数
// → 逐个建入站 → 放行防火墙 → 确认 Xray 在跑 → 打开 VLESS 流控、加进节点组、触发下发对账。
func (t *taskRun) deployInbounds(ctx context.Context, d deploy.Deployer, r remote.Runner, det deploy.Detection) error {
	if err := requireStandardPanel(det); err != nil {
		return err
	}
	if !t.host.ServerID.Valid {
		return deploy.NewError(deploy.ReasonCommandFailed, msgNeedsServer)
	}
	serverID := t.host.ServerID.Int64
	in, err := t.svc.presetPayload(ctx, t.id)
	if err != nil {
		return err
	}

	t.step(ctx, hostStepPorts, "检查主机上正在监听的端口")
	listening, err := d.ListeningPorts(ctx, r)
	if err != nil {
		return err
	}
	used, err := t.svc.usedPorts(ctx, serverID, listening)
	if err != nil {
		return err
	}
	if busy := busyPorts(in.Items, used); len(busy) > 0 {
		return deploy.NewError(deploy.ReasonPortInUse, msgPortsInUse+strings.Join(busy, "、"))
	}

	env := t.preparePresets(ctx, d, r, serverID, in.Items)
	t.step(ctx, hostStepInbounds, fmt.Sprintf("建 %d 个入站", len(in.Items)))
	results, err := t.createPresetInbounds(ctx, serverID, in.Items, env)
	if err != nil {
		return err
	}
	created := make([]presetResult, 0, len(results))
	for _, res := range results {
		if res.err == nil {
			created = append(created, res)
		}
	}
	if len(created) > 0 {
		if err := t.finishPresets(ctx, d, r, serverID, in.NodeGroupIDs, created); err != nil {
			t.logPresetSummary(results)
			return err
		}
	}
	t.logPresetSummary(results)
	return presetsError(results)
}

func (s *HostService) presetPayload(ctx context.Context, taskID int64) (InboundPresetDeployInput, error) {
	raw, err := s.q.GetHostTaskPayload(ctx, taskID)
	if err != nil {
		return InboundPresetDeployInput{}, fmt.Errorf("load payload of task %d: %w", taskID, err)
	}
	var in InboundPresetDeployInput
	if err := json.Unmarshal(raw, &in); err != nil || len(in.Items) == 0 {
		return InboundPresetDeployInput{}, deploy.NewError(deploy.ReasonCommandFailed, msgPresetNoItems)
	}
	return in, nil
}

// busyPorts 列出与已占用端口冲突的预设端口（如 “443/tcp”）。
func busyPorts(items []InboundPresetItem, used portUse) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if used.conflicts(it.Key, it.Port) {
			tcp, udp := inboundtpl.PresetPorts(it.Key)
			proto := "tcp"
			if udp && !tcp {
				proto = "udp"
			}
			out = append(out, strconv.Itoa(it.Port)+"/"+proto)
		}
	}
	return out
}

// preparePresets 准备证书、REALITY 目标与 ML-KEM 参数；只为选中的预设准备。
func (t *taskRun) preparePresets(ctx context.Context, d deploy.Deployer, r remote.Runner, serverID int64, items []InboundPresetItem) presetEnv {
	var env presetEnv
	has := func(match func(string) bool) bool {
		return slices.ContainsFunc(items, func(it InboundPresetItem) bool { return match(it.Key) })
	}
	if has(inboundtpl.PresetNeedsCert) {
		env.cert, env.certErr = t.presetCert(ctx, d, r, serverID)
	}
	isReality := func(k string) bool {
		return k == inboundtpl.PresetVLESSRealityVision || k == inboundtpl.PresetVLESSRealityMLKEM
	}
	if has(isReality) {
		env.target, env.realErr = t.presetRealityTarget(ctx, serverID)
	}
	if has(func(k string) bool { return k == inboundtpl.PresetVLESSRealityMLKEM }) {
		env.dec, env.enc, env.encErr = t.presetVLESSEncryption(ctx, serverID)
	}
	return env
}

// presetCert 确保主机上有平台签的域名证书（没有就先“配置证书”），返回面板上的证书文件路径。
func (t *taskRun) presetCert(ctx context.Context, d deploy.Deployer, r remote.Runner, serverID int64) (panel.CertFiles, error) {
	if !t.hasDNS() {
		return panel.CertFiles{}, deploy.NewError(deploy.ReasonCertFailed, msgPresetNoDNS)
	}
	if !t.host.CertNotAfter.Valid {
		t.logf("主机还没有域名证书，先配置证书")
		if err := t.certify(ctx, d, r, serverID); err != nil {
			return panel.CertFiles{}, err
		}
	}
	files, err := t.svc.panels.WebCertFiles(ctx, serverID)
	if err != nil {
		return panel.CertFiles{}, err
	}
	if files.CertFile == "" || files.KeyFile == "" {
		return panel.CertFiles{}, deploy.NewError(deploy.ReasonCertFailed, msgNoPanelCert)
	}
	t.logf("TLS 入站用证书 %s", files.CertFile)
	return files, nil
}

// presetRealityTarget 让面板扫描它的候选站点，取第一个可用的作 REALITY 目标。
func (t *taskRun) presetRealityTarget(ctx context.Context, serverID int64) (string, error) {
	scans, err := t.svc.panels.ScanRealityTargets(ctx, serverID, nil)
	if err != nil {
		return "", err
	}
	for _, sc := range scans {
		if !sc.Feasible || sc.Host == "" {
			continue
		}
		port := sc.Port
		if port == 0 {
			port = defaultRealityPort
		}
		target := net.JoinHostPort(sc.Host, strconv.Itoa(port))
		t.logf("REALITY 目标 %s（延迟 %d ms）", target, sc.LatencyMs)
		return target, nil
	}
	return "", deploy.NewError(deploy.ReasonCommandFailed, msgNoRealityTarget)
}

// presetVLESSEncryption 让面板生成一组 VLESS Encryption 参数，取 ML-KEM-768 那一组。
func (t *taskRun) presetVLESSEncryption(ctx context.Context, serverID int64) (string, string, error) {
	raw, err := t.svc.panels.GeneratePanelKeys(ctx, t.actor(), serverID, string(panel.KeyVLESSEnc), nil)
	if err != nil {
		return "", "", err
	}
	var keys struct {
		Auths []struct {
			ID         string `json:"id"`
			Decryption string `json:"decryption"`
			Encryption string `json:"encryption"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(raw, &keys); err != nil {
		return "", "", deploy.NewError(deploy.ReasonCommandFailed, msgNoMLKEM)
	}
	for _, a := range keys.Auths {
		if a.ID == vlessEncAuthMLKEM && a.Decryption != "" && a.Encryption != "" {
			return a.Decryption, a.Encryption, nil
		}
	}
	return "", "", deploy.NewError(deploy.ReasonCommandFailed, msgNoMLKEM)
}

// actor 是任务发起人（一键部署总有发起的管理员），写入面板时用它记审计。
func (t *taskRun) actor() *AdminPrincipal { return &AdminPrincipal{AdminID: t.adminID} }

// createPresetInbounds 逐个建入站；单项失败只记在这一项上。返回的 error 只表示读不到面板现有入站。
func (t *taskRun) createPresetInbounds(ctx context.Context, serverID int64, items []InboundPresetItem, env presetEnv) ([]presetResult, error) {
	existing, err := t.svc.panels.ListServerInbounds(ctx, serverID)
	if err != nil {
		return nil, err
	}
	remarks := make(map[string]bool, len(existing)+len(items))
	for _, in := range existing {
		remarks[in.Remark] = true
	}
	results := make([]presetResult, 0, len(items))
	for _, it := range items {
		res := presetResult{item: it, remark: inboundtpl.PresetRemark(it.Key)}
		if remarks[res.remark] {
			res.remark += "-" + strconv.Itoa(it.Port)
		}
		remarks[res.remark] = true
		res.inboundID, res.err = t.createPresetInbound(ctx, serverID, it, res.remark, env)
		if res.err == nil {
			res.nodeID, res.err = t.svc.panels.DirectNodeID(ctx, res.inboundID)
		}
		results = append(results, res)
	}
	return results, nil
}

func (t *taskRun) createPresetInbound(ctx context.Context, serverID int64, it InboundPresetItem, remark string, env presetEnv) (int64, error) {
	params := inboundtpl.PresetParams{Domain: t.host.Domain, CertFile: env.cert.CertFile, KeyFile: env.cert.KeyFile, RealityTarget: env.target,
		VLESSDecryption: env.dec, VLESSEncryption: env.enc}
	switch {
	case inboundtpl.PresetNeedsCert(it.Key) && env.certErr != nil:
		return 0, env.certErr
	case (it.Key == inboundtpl.PresetVLESSRealityVision || it.Key == inboundtpl.PresetVLESSRealityMLKEM) && env.realErr != nil:
		return 0, env.realErr
	case it.Key == inboundtpl.PresetVLESSRealityMLKEM && env.encErr != nil:
		return 0, env.encErr
	}
	spec, err := inboundtpl.BuildPreset(it.Key, inboundtpl.Base{Remark: remark, Port: it.Port, Enabled: true}, params)
	if err != nil {
		return 0, err
	}
	created, err := t.svc.panels.CreateInbound(ctx, t.actor(), serverID, InboundCreateInput{
		Remark: remark, Port: it.Port, Enabled: true, Protocol: spec.Protocol, Config: spec.Config,
	}, nil)
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

// finishPresets 收尾：放行防火墙、确认 Xray 在跑、打开 VLESS 流控、加进节点组并触发下发对账。
func (t *taskRun) finishPresets(ctx context.Context, d deploy.Deployer, r remote.Runner, serverID int64, groupIDs []int64, created []presetResult) error {
	t.step(ctx, hostStepFirewall, "放行防火墙端口（主机开着 ufw / firewalld 时）")
	ports := make([]deploy.Port, 0, 2*len(created))
	for _, res := range created {
		tcp, udp := inboundtpl.PresetPorts(res.item.Key)
		if tcp {
			ports = append(ports, deploy.Port{Number: res.item.Port})
		}
		if udp {
			ports = append(ports, deploy.Port{Number: res.item.Port, UDP: true})
		}
	}
	if err := d.OpenPorts(ctx, r, ports); err != nil {
		return err
	}

	t.step(ctx, hostStepXray, "确认面板的 Xray 在运行")
	if err := t.waitCoreRunning(ctx, serverID); err != nil {
		return err
	}

	if slices.ContainsFunc(created, func(res presetResult) bool { return inboundtpl.PresetWantsVision(res.item.Key) }) {
		_, err := t.svc.q.SetServerClientFlow(ctx, sqlcgen.SetServerClientFlowParams{ID: serverID, ClientFlow: ClientFlowVision})
		if err != nil {
			return fmt.Errorf("set client flow of server %d: %w", serverID, err)
		}
		t.logf("已打开服务器的 VLESS 流控（%s）", ClientFlowVision)
	}

	if len(groupIDs) > 0 {
		t.step(ctx, hostStepNodeGroups, "把新节点加进节点组")
		if err := t.addToNodeGroups(ctx, groupIDs, created); err != nil {
			return err
		}
	}
	if t.svc.reconciler != nil {
		t.svc.reconciler.TriggerReconcile()
	}
	return nil
}

// waitCoreRunning 要连续几次看到 Xray 在跑才算数；一直没在跑时带上面板给的错误说明失败。
func (t *taskRun) waitCoreRunning(ctx context.Context, serverID int64) error {
	streak, lastState, lastMsg := 0, "", ""
	// 至少要能看满连续几次
	for attempt := range max(t.svc.cfg.PanelWaitAttempts, coreRunningStreak) {
		if attempt > 0 {
			if err := sleepCtx(ctx, t.svc.cfg.PanelWaitInterval); err != nil {
				return err
			}
		}
		state, msg, err := t.svc.panels.CoreStatus(ctx, serverID)
		if err != nil {
			streak, lastMsg = 0, err.Error()
			continue
		}
		lastState, lastMsg = state, msg
		if state != panel.CoreRunning {
			streak = 0
			continue
		}
		streak++
		if streak >= coreRunningStreak {
			t.logf("Xray 在运行")
			return nil
		}
	}
	detail := strings.TrimSpace(lastState + " " + lastMsg)
	return deploy.NewError(deploy.ReasonXrayDown, msgXrayDown+"："+detail)
}

// addToNodeGroups 把新入站的直连节点并进所选节点组（组成员是整体替换，先合并现有成员）。
func (t *taskRun) addToNodeGroups(ctx context.Context, groupIDs []int64, created []presetResult) error {
	groups, err := t.svc.panels.ListNodeGroups(ctx)
	if err != nil {
		return err
	}
	for _, id := range groupIDs {
		idx := slices.IndexFunc(groups, func(g NodeGroup) bool { return g.ID == id })
		if idx < 0 {
			t.logf("节点组 %d 已不存在，跳过", id)
			continue
		}
		g := groups[idx]
		nodes := slices.Clone(g.NodeIDs)
		for _, res := range created {
			if !slices.Contains(nodes, res.nodeID) {
				nodes = append(nodes, res.nodeID)
			}
		}
		if err := t.svc.panels.UpdateNodeGroup(ctx, t.actor(), g.ID, NodeGroupInput{Name: g.Name, Remark: g.Remark, NodeIDs: nodes}, nil); err != nil {
			return err
		}
		t.logf("已加进节点组 %s", g.Name)
	}
	return nil
}

// logPresetSummary 逐项写结果。
func (t *taskRun) logPresetSummary(results []presetResult) {
	for _, res := range results {
		if res.err != nil {
			_, msg := taskFailure(res.err)
			t.logf("✗ %s 端口 %d：%s", res.remark, res.item.Port, msg)
			continue
		}
		t.logf("✓ %s 端口 %d（节点 %d）", res.remark, res.item.Port, res.nodeID)
	}
}

// presetsError 有项没建成时返回失败：原因大类取第一项失败的，摘要列出各项原因。
func presetsError(results []presetResult) error {
	var (
		first  deploy.Reason
		failed []string
	)
	for _, res := range results {
		if res.err == nil {
			continue
		}
		reason, msg := taskFailure(res.err)
		if first == deploy.ReasonNone {
			first = reason
		}
		failed = append(failed, res.remark+"："+msg)
	}
	if len(failed) == 0 {
		return nil
	}
	return deploy.NewError(first, fmt.Sprintf(msgPresetsFailed, len(results), len(failed), strings.Join(failed, "；")))
}
