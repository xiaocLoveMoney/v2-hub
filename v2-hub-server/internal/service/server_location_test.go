package service

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"v2-hub-server/internal/manager/geoip"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/panel/paneltest"
)

const fakeBaseB = "https://panel-b.example.com:2053/other"

// fakeLocator 按 IP 查表；不在表里视为查不到，down 为 true 时模拟 IP 库连不上。
type fakeLocator struct {
	codes map[string]string
	down  bool
	calls int
}

func (l *fakeLocator) Country(_ context.Context, ip netip.Addr) (string, error) {
	l.calls++
	if !geoip.IsPublic(ip) {
		return "", geoip.ErrNotPublic
	}
	if l.down {
		return "", geoip.ErrUnreachable
	}
	code, ok := l.codes[ip.String()]
	if !ok {
		return "", geoip.ErrUnknown
	}
	return code, nil
}

func TestServerLocation(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	loc := &fakeLocator{codes: map[string]string{paneltest.FakePublicIP: "US", "1.1.1.1": "AU"}}
	f.panels.cfg.Locator = loc

	in := serverInput("auto")
	in.Region = ""
	auto, err := f.panels.CreateServer(ctx, f.actor, in, nil)
	if err != nil || auto.Region != "US" {
		t.Fatalf("create with empty region = %+v, %v", auto, err)
	}
	picked := serverInput("picked")
	picked.BaseURL, picked.Region = fakeBaseB, "JP"
	srvB, err := f.panels.CreateServer(ctx, f.actor, picked, nil)
	if err != nil || srvB.Region != "JP" || loc.calls != 1 {
		t.Fatalf("create with region = %+v, %v, calls %d", srvB, err, loc.calls)
	}
	for _, bad := range []string{"XX", "jp", "美国", "USA"} {
		in := serverInput("bad")
		in.Region = bad
		if _, err := f.panels.UpdateServer(ctx, f.actor, srvB.ID, in, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("region %q err = %v", bad, err)
		}
	}

	// 手动扫描只返回结果，不改库
	f.fleet.get(fakeBase).SetPublicIP("1.1.1.1")
	got, err := f.panels.LocateServer(ctx, auto.ID)
	if err != nil || got.Region != "AU" || got.PublicIP != "1.1.1.1" {
		t.Fatalf("LocateServer = %+v, %v", got, err)
	}
	if s, _ := f.panels.GetServer(ctx, auto.ID); s.Region != "US" {
		t.Fatalf("locate must not save, region = %q", s.Region)
	}
	for _, ip := range []string{"", "10.0.0.1", "9.9.9.9"} {
		f.fleet.get(fakeBase).SetPublicIP(ip)
		if _, err := f.panels.LocateServer(ctx, auto.ID); !errors.Is(err, ErrConflict) {
			t.Fatalf("public ip %q err = %v", ip, err)
		}
	}
	loc.down = true
	f.fleet.get(fakeBase).SetPublicIP("1.1.1.1")
	if _, err := f.panels.LocateServer(ctx, auto.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("locator down err = %v", err)
	}
	loc.down = false
	f.fleet.get(fakeBase).FailWith(&panel.Error{Kind: panel.KindUnreachable, Message: "down"})
	if _, err := f.panels.LocateServer(ctx, auto.ID); !errors.Is(err, ErrUpstream) {
		t.Fatalf("panel down err = %v", err)
	}
	f.fleet.get(fakeBase).FailWith(nil)
	if _, err := f.panels.LocateServer(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown server err = %v", err)
	}

	// 启动补扫：只扫位置为空的启用服务器，且不覆盖已有位置
	if _, err := f.pool.Exec(ctx, "UPDATE server SET region = '' WHERE id = ANY($1)", []int64{auto.ID, srvB.ID}); err != nil {
		t.Fatal(err)
	}
	disabled := serverInput("picked")
	disabled.BaseURL, disabled.Region, disabled.IsDisabled = fakeBaseB, "", true
	if _, err := f.panels.UpdateServer(ctx, f.actor, srvB.ID, disabled, nil); err != nil {
		t.Fatalf("disable B: %v", err)
	}
	n, err := f.panels.BackfillServerLocations(ctx)
	if err != nil || n != 1 {
		t.Fatalf("backfill = %d, %v", n, err)
	}
	if s, _ := f.panels.GetServer(ctx, auto.ID); s.Region != "AU" {
		t.Fatalf("backfilled region = %q", s.Region)
	}
	if s, _ := f.panels.GetServer(ctx, srvB.ID); s.Region != "" {
		t.Fatalf("disabled server should be skipped, region = %q", s.Region)
	}

	// 扫描关闭
	f.panels.cfg.Locator = nil
	if _, err := f.panels.LocateServer(ctx, auto.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("disabled locate err = %v", err)
	}
	if n, err := f.panels.BackfillServerLocations(ctx); err != nil || n != 0 {
		t.Fatalf("disabled backfill = %d, %v", n, err)
	}
}

func TestServerMetrics(t *testing.T) {
	f := newSubFixture(t)
	ctx := context.Background()
	fake := f.fleet.get(fakeBase)
	f.clock.t = time.Date(2026, 11, 15, 10, 1, 0, 0, time.UTC)
	setOnline := func(n int) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, "UPDATE server SET online_clients = $2 WHERE id = $1", f.server.ID, n); err != nil {
			t.Fatal(err)
		}
	}
	probe := func(cpu float64, online int) {
		t.Helper()
		fake.SetCPU(cpu)
		setOnline(online)
		if err := f.panels.ProbeServer(ctx, f.server.ID); err != nil {
			t.Fatalf("ProbeServer: %v", err)
		}
	}
	probe(10, 1)
	f.clock.advance(time.Minute) // 同一个 5 分钟时段：覆盖为最新值
	probe(20, 3)
	f.clock.advance(10 * time.Minute)
	probe(40, 5)
	fake.FailWith(&panel.Error{Kind: panel.KindUnreachable, Message: "down"})
	f.clock.advance(10 * time.Minute)
	if err := f.panels.ProbeServer(ctx, f.server.ID); err == nil {
		t.Fatal("probe should fail")
	}
	fake.FailWith(nil)

	node := f.nodes[0].ID
	pts, err := f.users.ListNodeMetrics(ctx, f.principal, node, MetricRange24h)
	if err != nil || len(pts) != 2 {
		t.Fatalf("24h = %+v, %v", pts, err)
	}
	if !pts[0].At.Equal(time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)) || *pts[0].Load != 20 || pts[0].OnlineClients != 3 ||
		*pts[1].Load != 40 || pts[1].OnlineClients != 5 {
		t.Fatalf("24h points = %+v / %+v", pts[0], pts[1])
	}
	hourly, err := f.users.ListNodeMetrics(ctx, f.principal, node, MetricRange7d)
	if err != nil || len(hourly) != 1 || *hourly[0].Load != 30 || hourly[0].OnlineClients != 4 {
		t.Fatalf("7d = %+v, %v", hourly, err)
	}
	if _, err := f.users.ListNodeMetrics(ctx, f.principal, node, "1h"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bad range err = %v", err)
	}
	stranger := &UserPrincipal{UserID: f.principal.UserID + 1000, Status: UserStatusActive}
	if _, err := f.users.ListNodeMetrics(ctx, stranger, node, MetricRange24h); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger err = %v", err)
	}

	f.clock.advance(8 * 24 * time.Hour)
	if n, err := f.panels.PurgeServerMetrics(ctx); err != nil || n != 2 {
		t.Fatalf("purge = %d, %v", n, err)
	}
}
