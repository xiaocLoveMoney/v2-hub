package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"v2-hub-server/internal/manager/ipprobe"
	"v2-hub-server/internal/manager/ipquality"
	"v2-hub-server/internal/manager/remote"
)

const (
	ipqEgressIP  = "9.9.9.9"
	ipqMailAddr  = "127.0.0.1:2525"
	ipqLimit     = 3
	ipqKeepTwice = 1
)

// nxResolver 对一切查询回 NXDOMAIN（黑名单自检不过 → 不可用；Cymru 没有结果；不是 Tor 出口）。
type nxResolver struct{}

func nx(name string) error { return &net.DNSError{Name: name, Err: "no such host", IsNotFound: true} }

func (nxResolver) LookupHost(_ context.Context, h string) ([]string, error) { return nil, nx(h) }
func (nxResolver) LookupTXT(_ context.Context, h string) ([]string, error)  { return nil, nx(h) }
func (nxResolver) LookupMX(_ context.Context, h string) ([]*net.MX, error)  { return nil, nx(h) }

// probeOutput 是假主机上探测脚本的输出：出口 9.9.9.9 / CH，Netflix 完整解锁，Gmail 的 25 端口能连。
var probeOutput = strings.Join([]string{
	"tool.curl=1", "tool.telnet=1", "tool.bash=1",
	"eg.exit=0", "eg.w=200 104.16.0.1 https://www.cloudflare.com/cdn-cgi/trace", "eg.g.ip=ip=" + ipqEgressIP, "eg.g.loc=loc=CH",
	"nfo.exit=0", "nfo.w=200 54.0.0.1 https://www.netflix.com/ch/title/81280792", "nfo.m.oh=0",
	"nfl.exit=0", "nfl.w=200 54.0.0.1 https://www.netflix.com/ch/title/70143836", "nfl.m.oh=0",
	"mxgmail.exit=0", "mxgmail.code=220",
}, "\n")

type ipqFixture struct {
	*presetFixture
	svc      *IPQualityService
	settings *SettingsService
}

func newIPQFixture(t *testing.T, cfg IPQualityConfig) *ipqFixture {
	t.Helper()
	pf := newPresetFixture(t, false)
	pf.runner.Handle = func(script string) (remote.Result, error) {
		if strings.Contains(script, "tool.curl") {
			return remote.Result{Output: probeOutput}, nil
		}
		return remote.Result{}, nil
	}
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"country_code":"DE","region_name":"Hessen","city_name":"Frankfurt","asn":"64500","as":"Example","is_proxy":false}`))
	}))
	t.Cleanup(src.Close)
	reg, err := ipquality.NewRegistry(ipquality.Config{BaseURL: src.URL, Resolver: nxResolver{}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	prober, err := ipprobe.New(nil, ipprobe.Overrides{MailAddr: ipqMailAddr})
	if err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsService(pf.pool, SubscriptionConfig{}, pf.clock.now)
	cfg.Now = pf.clock.now
	svc := NewIPQualityService(pf.pool, IPQualityDeps{
		Panels: pf.panels, Hosts: pf.hosts, Settings: settings, Sources: reg, DNSBL: ipquality.NewDNSBL(nxResolver{}, pf.clock.now), Prober: prober,
	}, cfg)
	return &ipqFixture{presetFixture: pf, svc: svc, settings: settings}
}

// runNextCheck 领取并执行一条排队的检测，返回它的详情。
func (f *ipqFixture) runNextCheck(t *testing.T) *IPCheckDetail {
	t.Helper()
	ctx := context.Background()
	ids, err := f.svc.ClaimTasks(ctx, 1)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ClaimTasks = %v, err = %v", ids, err)
	}
	f.svc.RunTask(ctx, ids[0])
	d, err := f.svc.GetCheckDetail(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetCheckDetail: %v", err)
	}
	return d
}

func (f *ipqFixture) start(t *testing.T, actor *AdminPrincipal, serverID int64) *IPCheck {
	t.Helper()
	c, err := f.svc.StartCheck(context.Background(), actor, serverID, nil)
	if err != nil {
		t.Fatalf("StartCheck: %v", err)
	}
	return c
}

func decodeResult(t *testing.T, raw json.RawMessage) ipCheckResult {
	t.Helper()
	var r ipCheckResult
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode result: %v (%s)", err, raw)
	}
	return r
}

func TestIPCheckFullRun(t *testing.T) {
	f := newIPQFixture(t, IPQualityConfig{})
	ctx := context.Background()
	c := f.start(t, f.actor, f.serverID)
	if c.Status != IPCheckQueued || c.Scope != IPCheckScopeFull || c.Origin != IPCheckManual || c.CreatedByName != "root" {
		t.Fatalf("queued check = %+v", c)
	}
	if _, err := f.svc.StartCheck(ctx, f.actor, f.serverID, nil); err == nil {
		t.Fatal("second check while queued must be rejected")
	} else {
		wantCode(t, err, CodeConflict)
	}
	queued, err := f.svc.GetCheckDetail(ctx, c.ID)
	if err != nil || queued.Result != nil {
		t.Fatalf("queued detail = %+v, %v", queued, err)
	}

	d := f.runNextCheck(t)
	if d.Status != IPCheckSucceeded || d.HostProbe != HostProbeDone || d.IP != ipqEgressIP || d.IPSource != IPSourceHost {
		t.Fatalf("check = %+v", d.IPCheck)
	}
	if d.Country != "DE" || d.IPKind != ipquality.IPKindUnknown || d.ASN == nil || *d.ASN != 64500 || d.MediaTotal != 6 || d.MediaOK != 1 || d.Port25 != ipprobe.Port25Open {
		t.Fatalf("summary = %+v", d.IPCheck)
	}
	if d.DNSBLTotal != 0 || d.FinishedAt == nil {
		t.Fatalf("dnsbl/finished = %+v", d.IPCheck)
	}
	r := decodeResult(t, d.Result)
	st := r.Stacks[ipCheckStackV4]
	if r.Schema != ipCheckResultSchema || len(st.Sources) != 6 || st.Egress.Country != "CH" || st.Mail.Status != HostProbeDone {
		t.Fatalf("result = %s", d.Result)
	}
	for _, s := range st.Sources {
		if s.Key == ipquality.SourceIPQS && s.Status != ipquality.StatusNotConfigured {
			t.Errorf("ipqs status = %s", s.Status)
		}
	}
	var n int
	if err := f.pool.QueryRow(ctx, "SELECT COUNT(*) FROM audit_log WHERE action = $1", ActionIPCheckCreate).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d, %v", n, err)
	}
	page, err := f.svc.ListChecks(ctx, f.serverID, PageQuery{})
	if err != nil || page.Total != 1 || page.Items[0].ID != c.ID {
		t.Fatalf("ListChecks = %+v, %v", page, err)
	}
}

func TestIPCheckWithoutHost(t *testing.T) {
	f := newIPQFixture(t, IPQualityConfig{})
	ctx := context.Background()
	srv, err := f.panels.CreateServer(ctx, f.actor, serverInput("plain"), nil)
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	f.start(t, f.actor, srv.ID)
	d := f.runNextCheck(t)
	// 没绑 SSH：只做平台侧，IP 取面板报告的公网 IP
	if d.Status != IPCheckSucceeded || d.HostProbe != HostProbeNoHost || d.IPSource != IPSourcePanel || d.IP != "8.8.8.8" || d.MediaTotal != 0 || d.Port25 != "" {
		t.Fatalf("check = %+v", d.IPCheck)
	}
	st := decodeResult(t, d.Result).Stacks[ipCheckStackV4]
	if st.Media.Status != HostProbeNoHost || st.Media.Items == nil || len(st.Mail.Items) != 0 {
		t.Fatalf("host side = %+v / %+v", st.Media, st.Mail)
	}

	f.fleet.get(fakeBase).SetPublicIP("")
	f.start(t, f.actor, srv.ID)
	d = f.runNextCheck(t)
	if d.Status != IPCheckFailed || d.ErrorCode != IPCheckErrNoPublicIP || d.Result != nil {
		t.Fatalf("no public ip = %+v", d.IPCheck)
	}
}

func TestIPCheckDowngradesWithoutHostWrite(t *testing.T) {
	f := newIPQFixture(t, IPQualityConfig{})
	limited := &AdminPrincipal{AdminID: f.actor.AdminID, Permissions: PermissionSet{PermServerRead: {}, PermServerWrite: {}}}
	dials := len(f.dialer.Targets())
	c := f.start(t, limited, f.serverID)
	if c.Scope != IPCheckScopePlatform || c.HostProbe != HostProbeNoPermission {
		t.Fatalf("check = %+v", c)
	}
	d := f.runNextCheck(t)
	if d.Status != IPCheckSucceeded || d.HostProbe != HostProbeNoPermission || d.IPSource != IPSourcePanel {
		t.Fatalf("check = %+v", d.IPCheck)
	}
	if len(f.dialer.Targets()) != dials {
		t.Fatal("platform-only check must not dial the host")
	}
}

func TestIPCheckHostFailures(t *testing.T) {
	f := newIPQFixture(t, IPQualityConfig{})
	ctx := context.Background()

	f.dialer.Err = &remote.Error{Kind: remote.KindUnreachable, Message: "connection refused"}
	f.start(t, f.actor, f.serverID)
	if d := f.runNextCheck(t); d.Status != IPCheckSucceeded || d.HostProbe != HostProbeSSHFailed || d.IPSource != IPSourcePanel {
		t.Fatalf("ssh failed = %+v", d.IPCheck)
	}
	f.dialer.Err = nil

	f.runner.Handle = func(string) (remote.Result, error) { return remote.Result{Output: "tool.curl=0\n"}, nil }
	f.start(t, f.actor, f.serverID)
	if d := f.runNextCheck(t); d.HostProbe != HostProbeNoCurl {
		t.Fatalf("no curl = %+v", d.IPCheck)
	}

	if _, err := f.pool.Exec(ctx, "UPDATE server_host SET host_key_sha256 = '' WHERE id = $1", f.host.ID); err != nil {
		t.Fatal(err)
	}
	dials := len(f.dialer.Targets())
	f.start(t, f.actor, f.serverID)
	if d := f.runNextCheck(t); d.HostProbe != HostProbeHostUnverified || len(f.dialer.Targets()) != dials {
		t.Fatalf("unverified = %+v (dials %d → %d)", d.IPCheck, dials, len(f.dialer.Targets()))
	}
}

func TestIPCheckRateLimitAndInterrupted(t *testing.T) {
	f := newIPQFixture(t, IPQualityConfig{ManualLimit: ipqLimit, ManualWindow: time.Minute})
	ctx := context.Background()
	for i := 0; i < ipqLimit; i++ {
		f.start(t, f.actor, f.serverID)
		ids, err := f.svc.ClaimTasks(ctx, 1)
		if err != nil || len(ids) != 1 {
			t.Fatalf("claim: %v %v", ids, err)
		}
		if i < ipqLimit-1 {
			f.svc.RunTask(ctx, ids[0])
		}
	}
	// 最后一条领取了但没跑完：重启时记为中断
	if n, err := f.svc.FailInterrupted(ctx); err != nil || n != 1 {
		t.Fatalf("FailInterrupted = %d, %v", n, err)
	}
	page, err := f.svc.ListChecks(ctx, f.serverID, PageQuery{})
	if err != nil || page.Items[0].Status != IPCheckFailed || page.Items[0].ErrorCode != IPCheckErrInterrupted {
		t.Fatalf("latest = %+v, %v", page.Items[0], err)
	}
	if _, err := f.svc.StartCheck(ctx, f.actor, f.serverID, nil); err == nil {
		t.Fatal("rate limit not enforced")
	} else {
		wantCode(t, err, CodeRateLimited)
	}
	f.clock.advance(2 * time.Minute)
	f.start(t, f.actor, f.serverID)
}

func TestIPCheckScheduleAndPurge(t *testing.T) {
	f := newIPQFixture(t, IPQualityConfig{KeepPerServer: ipqKeepTwice})
	ctx := context.Background()
	if n, err := f.svc.QueueDueChecks(ctx); err != nil || n != 0 {
		t.Fatalf("disabled schedule queued %d, %v", n, err)
	}
	raw := map[string]json.RawMessage{"ipQuality.autoEnabled": json.RawMessage("true")}
	if _, err := f.settings.Update(ctx, f.actor, raw, nil); err != nil {
		t.Fatalf("enable schedule: %v", err)
	}
	if n, err := f.svc.QueueDueChecks(ctx); err != nil || n != 1 {
		t.Fatalf("schedule queued %d, %v", n, err)
	}
	if n, _ := f.svc.QueueDueChecks(ctx); n != 0 {
		t.Fatalf("server with an active check queued again: %d", n)
	}
	d := f.runNextCheck(t)
	if d.Origin != IPCheckAuto || d.Scope != IPCheckScopeFull || d.CreatedByName != "" || d.HostProbe != HostProbeDone {
		t.Fatalf("auto check = %+v", d.IPCheck)
	}
	f.start(t, f.actor, f.serverID)
	f.runNextCheck(t)
	if n, err := f.svc.PurgeChecks(ctx); err != nil || n != 1 {
		t.Fatalf("PurgeChecks = %d, %v", n, err)
	}
	if err := f.panels.DeleteServer(ctx, f.actor, f.serverID, nil); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	var left int
	if err := f.pool.QueryRow(ctx, "SELECT COUNT(*) FROM server_ip_check").Scan(&left); err != nil || left != 0 {
		t.Fatalf("checks left after server delete = %d, %v", left, err)
	}
}
