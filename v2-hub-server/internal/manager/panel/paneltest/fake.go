// Package paneltest 提供内存版的 panel.Provider，供 service 层测试使用，
// 同时验证业务层只依赖抽象、不依赖 3x-ui 细节（Ruling-8）。
package paneltest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"sync"

	"v2-hub-server/internal/manager/panel"
)

// Fake 是一台内存面板。可通过 FailWith 注入错误模拟离线或 token 失效。并发安全。
type Fake struct {
	mu        sync.Mutex
	info      panel.PanelInfo
	inbounds  map[string]panel.Inbound
	endpoints []panel.Endpoint
	clients   map[string]*panel.Client
	creds     map[string][2]string // email → {UUID, Auth}
	online    map[string]bool
	nextID    int
	nextGroup int
	failWith  error
	core      fakeCore
	// 出站测试：预设的结果与收到的请求；网页证书路径
	testResult panel.OutboundTestResult
	tests      []OutboundTest
	webCert    panel.CertFiles
	// Calls 记录调用的方法名，便于断言。
	Calls []string
}

// New 创建一台空面板。
func New() *Fake {
	return &Fake{
		info:     panel.PanelInfo{Version: "fake-1.0", GUID: "fake-guid", Status: json.RawMessage(`{"cpu":1.5}`)},
		inbounds: make(map[string]panel.Inbound, 8),
		clients:  make(map[string]*panel.Client, 8),
		creds:    make(map[string][2]string, 8),
		online:   make(map[string]bool, 8),
		nextID:   1,
		core:     newFakeCore(),
	}
}

// FailWith 让之后的所有调用返回 err；传 nil 恢复正常。
func (f *Fake) FailWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWith = err
}

// PutInbound 直接在“面板侧”写入一个入站（模拟有人绕过平台改面板）。
func (f *Fake) PutInbound(in panel.Inbound) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.ConfigHash == "" {
		in.ConfigHash = hashOf(in)
	}
	f.inbounds[in.Ref] = in
}

// RemoveInbound 直接在“面板侧”删除入站。
func (f *Fake) RemoveInbound(ref string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.inbounds, ref)
}

func hashOf(in panel.Inbound) string {
	h, err := panel.CanonicalHash(map[string]any{
		"protocol": in.Protocol, "port": in.Port, "listen": in.Listen, "remark": in.Remark,
		"enabled": in.Enabled, "config": in.Config,
	})
	if err != nil {
		return ""
	}
	return h
}

func (f *Fake) enter(method string) error {
	f.Calls = append(f.Calls, method)
	return f.failWith
}

// Capabilities 实现 panel.Provider。
func (f *Fake) Capabilities() panel.Capabilities {
	return panel.Capabilities{OnlineClients: true, CoreType: panel.CoreXray, Endpoints: true, MultiInboundClients: true, CoreConfig: true}
}

// Probe 实现 panel.Provider。
func (f *Fake) Probe(context.Context) (panel.PanelInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Probe"); err != nil {
		return panel.PanelInfo{}, err
	}
	return f.info, nil
}

// ListInbounds 实现 panel.Provider，按 Ref 数字顺序返回。
func (f *Fake) ListInbounds(context.Context) ([]panel.Inbound, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ListInbounds"); err != nil {
		return nil, err
	}
	out := make([]panel.Inbound, 0, len(f.inbounds))
	for _, in := range f.inbounds {
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i].Ref)
		b, _ := strconv.Atoi(out[j].Ref)
		return a < b
	})
	return out, nil
}

// GetInbound 实现 panel.Provider。
func (f *Fake) GetInbound(_ context.Context, ref string) (panel.Inbound, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("GetInbound"); err != nil {
		return panel.Inbound{}, err
	}
	in, ok := f.inbounds[ref]
	if !ok {
		return panel.Inbound{}, &panel.Error{Kind: panel.KindNotFound, Message: "入站不存在"}
	}
	return in, nil
}

func (f *Fake) portInUse(port int, except string) bool {
	for ref, in := range f.inbounds {
		if ref != except && in.Port == port {
			return true
		}
	}
	return false
}

// CreateInbound 实现 panel.Provider；端口冲突返回 KindRejected。
func (f *Fake) CreateInbound(_ context.Context, spec panel.InboundSpec) (panel.Inbound, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateInbound"); err != nil {
		return panel.Inbound{}, err
	}
	if f.portInUse(spec.Port, "") {
		return panel.Inbound{}, &panel.Error{Kind: panel.KindRejected, Message: "端口已被占用"}
	}
	ref := strconv.Itoa(f.nextID)
	f.nextID++
	in := fromSpec(ref, spec)
	f.inbounds[ref] = in
	return in, nil
}

func fromSpec(ref string, spec panel.InboundSpec) panel.Inbound {
	in := panel.Inbound{
		Ref: ref, Protocol: spec.Protocol, Port: spec.Port, Listen: spec.Listen, Remark: spec.Remark,
		Enabled: spec.Enabled, CoreType: panel.CoreXray, Config: spec.Config,
		Tag: fmt.Sprintf("in-%d", spec.Port), Network: "tcp",
	}
	in.ConfigHash = hashOf(in)
	return in
}

// UpdateInbound 实现 panel.Provider。
func (f *Fake) UpdateInbound(_ context.Context, ref string, spec panel.InboundSpec) (panel.Inbound, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("UpdateInbound"); err != nil {
		return panel.Inbound{}, err
	}
	cur, ok := f.inbounds[ref]
	if !ok {
		return panel.Inbound{}, &panel.Error{Kind: panel.KindNotFound, Message: "入站不存在"}
	}
	if f.portInUse(spec.Port, ref) {
		return panel.Inbound{}, &panel.Error{Kind: panel.KindRejected, Message: "端口已被占用"}
	}
	if len(spec.Config) == 0 {
		spec.Config = cur.Config
	}
	in := fromSpec(ref, spec)
	in.ClientCount = cur.ClientCount
	f.inbounds[ref] = in
	return in, nil
}

// SetInboundEnabled 实现 panel.Provider。
func (f *Fake) SetInboundEnabled(_ context.Context, ref string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetInboundEnabled"); err != nil {
		return err
	}
	in, ok := f.inbounds[ref]
	if !ok {
		return &panel.Error{Kind: panel.KindNotFound, Message: "入站不存在"}
	}
	in.Enabled = enabled
	in.ConfigHash = hashOf(in)
	f.inbounds[ref] = in
	return nil
}

// GetInboundConfig 实现 panel.Provider：内存面板不删密钥，原样返回 Config。
func (f *Fake) GetInboundConfig(_ context.Context, ref string) (panel.InboundConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("GetInboundConfig"); err != nil {
		return panel.InboundConfig{}, err
	}
	in, ok := f.inbounds[ref]
	if !ok {
		return panel.InboundConfig{}, &panel.Error{Kind: panel.KindNotFound, Message: "入站不存在"}
	}
	return panel.InboundConfig{Protocol: in.Protocol, Port: in.Port, Listen: in.Listen, Remark: in.Remark, Enabled: in.Enabled, Config: in.Config}, nil
}

// FakeX25519 是内存面板生成的固定 x25519 密钥对，便于断言。
const FakeX25519 = `{"privateKey":"fake-private","publicKey":"fake-public"}`

// GenerateKeys 实现 panel.Provider：x25519 返回 FakeX25519，其余种类返回空对象。
func (f *Fake) GenerateKeys(_ context.Context, kind panel.KeyKind) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("GenerateKeys"); err != nil {
		return nil, err
	}
	if kind == panel.KeyX25519 {
		return json.RawMessage(FakeX25519), nil
	}
	return json.RawMessage(`{}`), nil
}

// DeleteInbound 实现 panel.Provider；不存在也视为成功（与 3x-ui 行为一致）。
func (f *Fake) DeleteInbound(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DeleteInbound"); err != nil {
		return err
	}
	delete(f.inbounds, ref)
	kept := f.endpoints[:0]
	for _, e := range f.endpoints {
		if e.InboundRef != ref {
			kept = append(kept, e)
		}
	}
	f.endpoints = kept
	return nil
}

// ListEndpoints 实现 panel.Provider。
func (f *Fake) ListEndpoints(context.Context) ([]panel.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ListEndpoints"); err != nil {
		return nil, err
	}
	out := make([]panel.Endpoint, len(f.endpoints))
	copy(out, f.endpoints)
	return out, nil
}

// CreateEndpoints 实现 panel.Provider：为每个入站 × 地址生成一行，同组共享 GroupRef。
func (f *Fake) CreateEndpoints(_ context.Context, spec panel.EndpointSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateEndpoints"); err != nil {
		return err
	}
	f.nextGroup++
	group := fmt.Sprintf("g%d", f.nextGroup)
	for _, ref := range spec.InboundRefs {
		if _, ok := f.inbounds[ref]; !ok {
			return &panel.Error{Kind: panel.KindRejected, Message: "入站不存在"}
		}
		for _, addr := range spec.Addresses {
			f.endpoints = append(f.endpoints, panel.Endpoint{
				GroupRef: group, InboundRef: ref, Address: addr, Port: spec.Port, Remark: spec.Remark,
			})
		}
	}
	return nil
}

// RemoveEndpoint 实现 panel.Provider，语义同 3x-ui 实现：组是“入站集合 × 地址集合”，
// 删一对后剩下的若不再是完整的乘积，就把本入站的其余地址或其它入站上的该地址（取少者）挪到新组。
func (f *Fake) RemoveEndpoint(_ context.Context, key panel.EndpointKey) ([]panel.EndpointMove, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("RemoveEndpoint"); err != nil {
		return nil, err
	}
	found := false
	refs, addrs := map[string]bool{}, map[string]bool{}
	for _, e := range f.endpoints {
		if e.GroupRef != key.GroupRef {
			continue
		}
		refs[e.InboundRef], addrs[e.Address] = true, true
		found = found || (e.InboundRef == key.InboundRef && e.Address == key.Address)
	}
	if !found {
		return nil, &panel.Error{Kind: panel.KindNotFound, Message: "面板上已没有这个对外地址"}
	}
	otherRefs, keptAddrs := len(refs)-1, len(addrs)-1
	splitting := otherRefs > 0 && keptAddrs > 0
	moveOwnRest := keptAddrs <= otherRefs
	newGroup := ""
	if splitting {
		f.nextGroup++
		newGroup = fmt.Sprintf("g%d", f.nextGroup)
	}
	var moves []panel.EndpointMove
	kept := f.endpoints[:0]
	for _, e := range f.endpoints {
		inGroup := e.GroupRef == key.GroupRef
		ownInbound, sameAddr := e.InboundRef == key.InboundRef, e.Address == key.Address
		if inGroup && ownInbound && sameAddr {
			continue
		}
		if inGroup && splitting && ((moveOwnRest && ownInbound) || (!moveOwnRest && sameAddr)) {
			e.GroupRef = newGroup
			moves = append(moves, panel.EndpointMove{InboundRef: e.InboundRef, Address: e.Address, NewGroupRef: newGroup})
		}
		kept = append(kept, e)
	}
	f.endpoints = kept
	return moves, nil
}

// SetNextRef 指定下一个新建入站的标识，模拟面板复用已删入站的 id（docs/03 §5.1 面板标识复用）。
func (f *Fake) SetNextRef(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID = n
}

// AddTraffic 模拟用户产生流量：给面板上该 client 的累计计数加上 up / down。
func (f *Fake) AddTraffic(email string, up, down int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.clients[email]; ok {
		c.Up += up
		c.Down += down
	}
}

// ResetTraffic 模拟面板侧把 client 的流量计数清零。
func (f *Fake) ResetTraffic(email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.clients[email]; ok {
		c.Up, c.Down = 0, 0
	}
}

// ClientSnapshot 返回面板上某个 client 的副本，便于断言；不存在时 ok 为 false。
func (f *Fake) ClientSnapshot(email string) (panel.Client, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.clients[email]
	if !ok {
		return panel.Client{}, false
	}
	out := *c
	out.InboundRefs = append([]string(nil), c.InboundRefs...)
	sort.Strings(out.InboundRefs)
	return out, true
}

// ListClients 实现 panel.Provider，按 email 排序。
func (f *Fake) ListClients(context.Context) ([]panel.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ListClients"); err != nil {
		return nil, err
	}
	out := make([]panel.Client, 0, len(f.clients))
	for _, c := range f.clients {
		cp := *c
		cp.InboundRefs = append([]string(nil), c.InboundRefs...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out, nil
}

func (f *Fake) checkRefs(refs []string) error {
	for _, ref := range refs {
		if _, ok := f.inbounds[ref]; !ok {
			return &panel.Error{Kind: panel.KindRejected, Message: "入站不存在：" + ref}
		}
	}
	return nil
}

// AddClient 实现 panel.Provider。
func (f *Fake) AddClient(_ context.Context, spec panel.ClientSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("AddClient"); err != nil {
		return err
	}
	if _, ok := f.clients[spec.Email]; ok {
		return &panel.Error{Kind: panel.KindRejected, Message: "client 已存在"}
	}
	if err := f.checkRefs(spec.InboundRefs); err != nil {
		return err
	}
	f.clients[spec.Email] = &panel.Client{Email: spec.Email, Enabled: spec.Enabled, LimitIP: spec.LimitIP, InboundRefs: append([]string(nil), spec.InboundRefs...)}
	f.creds[spec.Email] = [2]string{spec.UUID, spec.Auth}
	return nil
}

func (f *Fake) client(email string) (*panel.Client, error) {
	c, ok := f.clients[email]
	if !ok {
		return nil, &panel.Error{Kind: panel.KindRejected, Message: "client 不存在"}
	}
	return c, nil
}

// UpdateClient 实现 panel.Provider。
func (f *Fake) UpdateClient(_ context.Context, spec panel.ClientSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("UpdateClient"); err != nil {
		return err
	}
	c, err := f.client(spec.Email)
	if err != nil {
		return err
	}
	c.Enabled, c.LimitIP = spec.Enabled, spec.LimitIP
	return nil
}

// AttachClient 实现 panel.Provider。
func (f *Fake) AttachClient(_ context.Context, email string, refs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("AttachClient"); err != nil {
		return err
	}
	c, err := f.client(email)
	if err != nil {
		return err
	}
	if err := f.checkRefs(refs); err != nil {
		return err
	}
	for _, r := range refs {
		if !slices.Contains(c.InboundRefs, r) {
			c.InboundRefs = append(c.InboundRefs, r)
		}
	}
	return nil
}

// DetachClient 实现 panel.Provider。
func (f *Fake) DetachClient(_ context.Context, email string, refs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DetachClient"); err != nil {
		return err
	}
	c, err := f.client(email)
	if err != nil {
		return err
	}
	c.InboundRefs = slices.DeleteFunc(c.InboundRefs, func(r string) bool { return slices.Contains(refs, r) })
	return nil
}

// SetClientsEnabled 实现 panel.Provider。
func (f *Fake) SetClientsEnabled(_ context.Context, emails []string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetClientsEnabled"); err != nil {
		return err
	}
	for _, e := range emails {
		c, err := f.client(e)
		if err != nil {
			return err
		}
		c.Enabled = enabled
	}
	return nil
}

// DeleteClient 实现 panel.Provider。
func (f *Fake) DeleteClient(_ context.Context, email string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DeleteClient"); err != nil {
		return err
	}
	if _, err := f.client(email); err != nil {
		return err
	}
	delete(f.clients, email)
	delete(f.creds, email)
	delete(f.online, email)
	return nil
}

var _ panel.Provider = (*Fake)(nil)

// FakeHost 是内存面板在没有对外地址时给分享链接用的地址。
const FakeHost = "fake.panel.test"

// SetClientCredential 实现 panel.Provider。
func (f *Fake) SetClientCredential(_ context.Context, spec panel.ClientSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetClientCredential"); err != nil {
		return err
	}
	c, err := f.client(spec.Email)
	if err != nil {
		return err
	}
	c.Enabled, c.LimitIP = spec.Enabled, spec.LimitIP
	f.creds[spec.Email] = [2]string{spec.UUID, spec.Auth}
	return nil
}

// ClientLinks 实现 panel.Provider：每个挂载的入站按其对外地址各出一条链接，没有对外地址时用 FakeHost。
// 只为 vless、trojan 生成链接，其余协议不出链接（与 3x-ui 对无链接形式协议的处理一致）。
func (f *Fake) ClientLinks(_ context.Context, email string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ClientLinks"); err != nil {
		return nil, err
	}
	c, err := f.client(email)
	if err != nil {
		return nil, err
	}
	cred := f.creds[email]
	refs := append([]string(nil), c.InboundRefs...)
	sort.Strings(refs)
	links := make([]string, 0, len(refs))
	for _, ref := range refs {
		in := f.inbounds[ref]
		secret := cred[0]
		if in.Protocol == "trojan" {
			secret = cred[1]
		}
		if in.Protocol != "vless" && in.Protocol != "trojan" {
			continue
		}
		hosts := make([][2]string, 0, 2)
		for _, ep := range f.endpoints {
			if ep.InboundRef != ref || ep.Disabled {
				continue
			}
			port := ep.Port
			if port == 0 {
				port = in.Port
			}
			hosts = append(hosts, [2]string{ep.Address, fmt.Sprint(port)})
		}
		if len(hosts) == 0 {
			hosts = append(hosts, [2]string{FakeHost, fmt.Sprint(in.Port)})
		}
		for _, h := range hosts {
			links = append(links, fmt.Sprintf("%s://%s@%s:%s?type=tcp&security=none#%s-%s", in.Protocol, secret, h[0], h[1], in.Remark, email))
		}
	}
	return links, nil
}

// SetOnline 设置当前在线的 client（测试用）。
func (f *Fake) SetOnline(emails ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online = make(map[string]bool, len(emails))
	for _, e := range emails {
		f.online[e] = true
	}
}

// OnlineClients 实现 panel.Provider，按 email 排序。
func (f *Fake) OnlineClients(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("OnlineClients"); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(f.online))
	for e := range f.online {
		out = append(out, e)
	}
	sort.Strings(out)
	return out, nil
}
