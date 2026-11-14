package paneltest

import (
	"context"
	"encoding/json"
	"strings"

	"v2-hub-server/internal/manager/panel"
)

// DefaultCoreTemplate 是假面板初始的内核配置模板（与 3x-ui 默认模板同构的精简版）。
const DefaultCoreTemplate = `{"log":{"loglevel":"warning"},"inbounds":[{"tag":"api","port":62789,"protocol":"tunnel"}],` +
	`"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"}],` +
	`"routing":{"domainStrategy":"AsIs","rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"},` +
	`{"type":"field","ip":["geoip:private"],"outboundTag":"blocked"}]}}`

const fakeTestURL = "https://example.test/generate_204"

// fakeCore 是假面板的内核：模板、运行状态，以及让某些模板启动失败的开关。
type fakeCore struct {
	template json.RawMessage
	extra    map[string]string
	running  bool
	// breakOn 非空时，模板里含这个片段就会让重启后的内核处于未运行状态（模拟配置有误）
	breakOn string
	// rejectOn 非空时，含这个片段的模板在保存时就被拒绝（模拟 3x-ui 保存前的校验）
	rejectOn string
	restarts int
}

func newFakeCore() fakeCore {
	return fakeCore{
		template: json.RawMessage(DefaultCoreTemplate),
		extra:    map[string]string{"outboundTestUrl": fakeTestURL},
		running:  true,
	}
}

// BreakCoreOn 让含 fragment 的模板在重启后启动失败；传空串恢复。
func (f *Fake) BreakCoreOn(fragment string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.core.breakOn = fragment
}

// RejectTemplateOn 让含 fragment 的模板在保存时被拒绝、模板保持不变；传空串恢复。
func (f *Fake) RejectTemplateOn(fragment string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.core.rejectOn = fragment
}

// CoreSnapshot 返回当前模板、附带设置里的出站测试地址、是否运行与重启次数。
func (f *Fake) CoreSnapshot() (json.RawMessage, string, bool, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append(json.RawMessage(nil), f.core.template...), f.core.extra["outboundTestUrl"], f.core.running, f.core.restarts
}

// PutCoreTemplate 直接在“面板侧”改模板（模拟有人在面板上手改）。
func (f *Fake) PutCoreTemplate(tpl json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.core.template = append(json.RawMessage(nil), tpl...)
}

// GetCoreTemplate 实现 panel.CoreConfigurator。
func (f *Fake) GetCoreTemplate(context.Context) (panel.CoreTemplate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("GetCoreTemplate"); err != nil {
		return panel.CoreTemplate{}, err
	}
	extra := make(map[string]string, len(f.core.extra))
	for k, v := range f.core.extra {
		extra[k] = v
	}
	return panel.CoreTemplate{Config: append(json.RawMessage(nil), f.core.template...), Extra: extra}, nil
}

// SetCoreTemplate 实现 panel.CoreConfigurator；附带设置缺了出站测试地址时与真面板一样重置为空。
func (f *Fake) SetCoreTemplate(_ context.Context, tpl panel.CoreTemplate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetCoreTemplate"); err != nil {
		return err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(tpl.Config, &probe); err != nil {
		return &panel.Error{Kind: panel.KindRejected, Message: "模板不是 JSON 对象"}
	}
	if f.core.rejectOn != "" && strings.Contains(string(tpl.Config), f.core.rejectOn) {
		return &panel.Error{Kind: panel.KindRejected, Message: "面板拒绝了请求：xray core rejects outbound"}
	}
	f.core.template = append(json.RawMessage(nil), tpl.Config...)
	f.core.extra = map[string]string{"outboundTestUrl": tpl.Extra["outboundTestUrl"]}
	return nil
}

// RestartCore 实现 panel.CoreConfigurator。
func (f *Fake) RestartCore(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RestartCore"); err != nil {
		return err
	}
	f.core.restarts++
	f.core.running = f.core.breakOn == "" || !strings.Contains(string(f.core.template), f.core.breakOn)
	return nil
}

// CoreState 实现 panel.CoreConfigurator。
func (f *Fake) CoreState(context.Context) (panel.CoreState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CoreState"); err != nil {
		return panel.CoreState{}, err
	}
	if f.core.running {
		return panel.CoreState{Running: true, State: "running"}, nil
	}
	return panel.CoreState{State: "error", Message: "failed to start: fake config error"}, nil
}

// OutboundTest 记录假面板收到的一次出站测试。
type OutboundTest struct {
	Outbound json.RawMessage
	Mode     panel.OutboundTestMode
}

// SetOutboundTestResult 设定之后出站测试返回的结果。
func (f *Fake) SetOutboundTestResult(r panel.OutboundTestResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.testResult = r
}

// SetOutboundTestResultFor 设定某一种方式的出站测试结果，优先于 SetOutboundTestResult 设的那份。
func (f *Fake) SetOutboundTestResultFor(mode panel.OutboundTestMode, r panel.OutboundTestResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.testResultByMode == nil {
		f.testResultByMode = make(map[panel.OutboundTestMode]panel.OutboundTestResult, 3)
	}
	f.testResultByMode[mode] = r
}

// OutboundTests 返回假面板收到过的出站测试。
func (f *Fake) OutboundTests() []OutboundTest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OutboundTest(nil), f.tests...)
}

// SetWebCert 设定假面板的网页证书路径。
func (f *Fake) SetWebCert(files panel.CertFiles) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.webCert = files
}

// TestOutbound 实现 panel.OutboundTester：记下请求，返回预设的结果（按方式设的优先；Mode 缺省取请求的方式）。
func (f *Fake) TestOutbound(_ context.Context, outbound json.RawMessage, mode panel.OutboundTestMode) (panel.OutboundTestResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("TestOutbound"); err != nil {
		return panel.OutboundTestResult{}, err
	}
	f.tests = append(f.tests, OutboundTest{Outbound: append(json.RawMessage(nil), outbound...), Mode: mode})
	out, ok := f.testResultByMode[mode]
	if !ok {
		out = f.testResult
	}
	if out.Mode == "" {
		out.Mode = mode
	}
	return out, nil
}

// WebCertFiles 实现 panel.CertLocator。
func (f *Fake) WebCertFiles(context.Context) (panel.CertFiles, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("WebCertFiles"); err != nil {
		return panel.CertFiles{}, err
	}
	return f.webCert, nil
}

// FakeRealityCandidate 是假面板自己的候选站点（不带目标的批量扫描返回它）。
const FakeRealityCandidate = "www.example.org:443"

// SetRealityScan 设定某个目标的扫描结果；没设过的目标返回“不可用”。
func (f *Fake) SetRealityScan(r panel.RealityScan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scans == nil {
		f.scans = make(map[string]panel.RealityScan, 4)
	}
	f.scans[r.Target] = r
}

// RealityScanTargets 返回假面板收到过的每次扫描的目标列表。
func (f *Fake) RealityScanTargets() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.scanTargets...)
}

func (f *Fake) scanOf(target string) panel.RealityScan {
	if r, ok := f.scans[target]; ok {
		return r
	}
	return panel.RealityScan{Target: target, Reason: "no tls 1.3"}
}

// ScanRealityTarget 实现 panel.RealityScanner。
func (f *Fake) ScanRealityTarget(_ context.Context, target, _ string, _ int) (panel.RealityScan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ScanRealityTarget"); err != nil {
		return panel.RealityScan{}, err
	}
	f.scanTargets = append(f.scanTargets, []string{target})
	return f.scanOf(target), nil
}

// ScanRealityTargets 实现 panel.RealityScanner；不带目标时返回假面板自己的候选站点。
func (f *Fake) ScanRealityTargets(_ context.Context, targets []string) ([]panel.RealityScan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ScanRealityTargets"); err != nil {
		return nil, err
	}
	f.scanTargets = append(f.scanTargets, append([]string(nil), targets...))
	if len(targets) == 0 {
		targets = []string{FakeRealityCandidate}
	}
	out := make([]panel.RealityScan, 0, len(targets))
	for _, t := range targets {
		out = append(out, f.scanOf(t))
	}
	return out, nil
}
