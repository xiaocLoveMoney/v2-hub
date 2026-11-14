package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/render"
)

const (
	// HTTP / HTTPS 代理出口的示例链接（Ruling-51）；用户名与口令是示例值。
	ownHTTPLink  = "http://sample-user:sample-auth-0001@203.0.114.21:8080#office-http"
	ownHTTPSLink = "https://home.example.net:8443#office-https"
	sqlSetExitOn = `UPDATE chain_exit SET is_enabled = $2 WHERE id = $1`
	testExitMax  = 1000
)

// newExitTestFixture 在下发夹具上把测试频率放宽；专门测频控的用例另建服务。
func newExitTestFixture(t *testing.T) *chainApplyFixture {
	t.Helper()
	f := newChainApplyFixture(t)
	f.chain = NewChainService(f.pool, testSealer(t), f.applier, ChainOptions{
		Resolver: f.resolver, Now: f.clock.now, ServerWriteLimit: testWriteMax, ExitTestLimit: testExitMax,
	})
	return f
}

func TestExitTest(t *testing.T) {
	f := newExitTestFixture(t)
	ctx := context.Background()
	first := f.nodes[0]
	f.fake.SetOutboundTestResult(panel.OutboundTestResult{
		OK: true, DelayMs: 87, HTTPStatus: 204, EgressIP: "198.51.100.7", EgressRegion: "JP", Message: "panel detail",
	})

	// 域名出口：面板收到的出站用固定后的 IP，域名不进出站地址；结果不带面板的错误原文（Ruling-52）
	home := f.mustExit(t, chainSocksLink, chainExitName)
	res, err := f.chain.TestExit(ctx, f.principal, home.ID, first.ID, string(panel.OutboundTestHTTP))
	if err != nil {
		t.Fatalf("TestExit: %v", err)
	}
	want := ExitTestResult{OK: true, Mode: "http", DelayMs: 87, HTTPStatus: 204, EgressIP: "198.51.100.7", EgressRegion: "JP"}
	if *res != want {
		t.Errorf("result = %+v, want %+v", *res, want)
	}
	tests := f.fake.OutboundTests()
	if len(tests) != 1 || tests[0].Mode != panel.OutboundTestHTTP {
		t.Fatalf("panel got %+v", tests)
	}
	sent := string(tests[0].Outbound)
	isPinned := strings.Contains(sent, guardPublicV4) && !strings.Contains(sent, "home.example.net") && strings.Contains(sent, `"tag":"`+exitTestTag+`"`)
	if !isPinned {
		t.Errorf("test outbound is not pinned: %s", sent)
	}
	// 测试不落库固定的地址，也不动面板模板
	if pin := f.pinOf(t, home.ID); pin != "" {
		t.Errorf("test stored a pin: %q", pin)
	}

	// 三种方式原样传给面板；未知方式 400
	for _, mode := range []panel.OutboundTestMode{panel.OutboundTestTCP, panel.OutboundTestReal} {
		if _, err := f.chain.TestExit(ctx, f.principal, home.ID, first.ID, string(mode)); err != nil {
			t.Fatalf("TestExit %s: %v", mode, err)
		}
	}
	if got := f.fake.OutboundTests(); len(got) != 3 || got[1].Mode != panel.OutboundTestTCP || got[2].Mode != panel.OutboundTestReal {
		t.Errorf("modes sent = %+v", got)
	}
	_, err = f.chain.TestExit(ctx, f.principal, home.ID, first.ID, "icmp")
	wantErr(t, "unknown mode", err, ErrInvalidArgument)

	// 连不上：照常返回结果，ok 为 false，原因不外泄
	f.fake.SetOutboundTestResult(panel.OutboundTestResult{Message: "dial tcp 93.184.216.34:1080: i/o timeout"})
	res, err = f.chain.TestExit(ctx, f.principal, home.ID, first.ID, string(panel.OutboundTestTCP))
	if err != nil || res.OK || res.Error != "" || res.DelayMs != 0 {
		t.Errorf("failed probe: %+v err %v", res, err)
	}
}

func TestExitTestGuards(t *testing.T) {
	f := newExitTestFixture(t)
	ctx := context.Background()
	first := f.nodes[0]
	const mode = string(panel.OutboundTestTCP)
	before := len(f.fake.OutboundTests())

	// 回环、解析到内网、解析不了、指向平台自己服务器的出口都不许测（面板会去连这个地址）
	for what, link := range map[string]string{"loopback": ownLoopLink, "inner domain": ownInnerLink, "unresolvable": ownGoneLink, "platform server": ownPanelLink} {
		exit := f.mustExit(t, link, what)
		_, err := f.chain.TestExit(ctx, f.principal, exit.ID, first.ID, mode)
		wantErr(t, what, err, ErrInvalidArgument)
	}
	public := f.mustExit(t, ownPublicLink, "public")
	// 起点必须是本人可用的节点
	_, err := f.chain.TestExit(ctx, f.principal, public.ID, first.ID+999, mode)
	wantErr(t, "unknown node", err, ErrInvalidArgument)
	// R8：别人的出口、平台出口、不存在的出口一律 404
	bob := f.newUser(t, "bob", *f.user.PlanID)
	bobP := &UserPrincipal{UserID: bob.ID, Username: bob.Username, Status: UserStatusActive}
	_, err = f.chain.TestExit(ctx, bobP, public.ID, first.ID, mode)
	wantErr(t, "another user's exit", err, ErrNotFound)
	platform := f.platformExit(t, chainPlatformName, true, true)
	_, err = f.chain.TestExit(ctx, f.principal, platform.ID, first.ID, mode)
	wantErr(t, "platform exit via user api", err, ErrNotFound)
	_, err = f.chain.TestExit(ctx, f.principal, public.ID+999, first.ID, mode)
	wantErr(t, "missing exit", err, ErrNotFound)

	// 被管理员停用的出口不能测；套餐不再允许自己的出口时也不能
	if _, err := f.pool.Exec(ctx, sqlSetExitOn, public.ID, false); err != nil {
		t.Fatalf("disable exit: %v", err)
	}
	_, err = f.chain.TestExit(ctx, f.principal, public.ID, first.ID, mode)
	wantErr(t, "disabled exit", err, ErrConflict)
	if _, err := f.pool.Exec(ctx, sqlSetExitOn, public.ID, true); err != nil {
		t.Fatalf("enable exit: %v", err)
	}
	f.setFlags(t, true, false)
	_, err = f.chain.TestExit(ctx, f.principal, public.ID, first.ID, mode)
	wantErr(t, "plan without own exits", err, ErrFeatureDisabled)
	f.setFlags(t, true, true)

	if got := len(f.fake.OutboundTests()); got != before {
		t.Fatalf("refused tests reached the panel: %d calls", got-before)
	}

	// 同一台服务器已有测试在跑：409
	if !f.chain.testing.tryLock(f.server.ID) {
		t.Fatal("server lock unexpectedly held")
	}
	_, err = f.chain.TestExit(ctx, f.principal, public.ID, first.ID, mode)
	wantErr(t, "busy server", err, ErrConflict)
	f.chain.testing.unlock(f.server.ID)
	if _, err := f.chain.TestExit(ctx, f.principal, public.ID, first.ID, mode); err != nil {
		t.Fatalf("test after the lock is released: %v", err)
	}

	// 面板连不上：502 类错误，不是测试结果
	f.fake.FailWith(panel.ErrUnreachable)
	_, err = f.chain.TestExit(ctx, f.principal, public.ID, first.ID, mode)
	wantErr(t, "panel unreachable", err, ErrUpstream)
	f.fake.FailWith(nil)
}

func TestExitTestRateLimit(t *testing.T) {
	f := newChainApplyFixture(t)
	ctx := context.Background()
	const limit = 2
	f.chain = NewChainService(f.pool, testSealer(t), f.applier, ChainOptions{
		Resolver: f.resolver, Now: f.clock.now, ServerWriteLimit: testWriteMax, ExitTestLimit: limit, ExitTestWindow: time.Minute,
	})
	public := f.mustExit(t, ownPublicLink, "public")
	first := f.nodes[0]
	const mode = string(panel.OutboundTestTCP)
	for i := range limit {
		if _, err := f.chain.TestExit(ctx, f.principal, public.ID, first.ID, mode); err != nil {
			t.Fatalf("test %d: %v", i+1, err)
		}
	}
	_, err := f.chain.TestExit(ctx, f.principal, public.ID, first.ID, mode)
	wantErr(t, "test over the limit", err, ErrRateLimited)
	// 管理员的测试不受用户频控影响
	if _, err := f.chain.TestExitAsAdmin(ctx, public.ID, f.server.ID, mode); err != nil {
		t.Fatalf("admin test: %v", err)
	}
	f.clock.advance(time.Minute + time.Second)
	if _, err := f.chain.TestExit(ctx, f.principal, public.ID, first.ID, mode); err != nil {
		t.Fatalf("test after the window: %v", err)
	}
}

func TestExitTestAsAdmin(t *testing.T) {
	f := newExitTestFixture(t)
	ctx := context.Background()
	const mode = string(panel.OutboundTestHTTP)
	f.fake.SetOutboundTestResult(panel.OutboundTestResult{Message: "connection refused"})

	// 平台出口不受地址限制，原样下发；管理员看得到错误原文
	platform := f.platformExit(t, chainPlatformName, true, false)
	res, err := f.chain.TestExitAsAdmin(ctx, platform.ID, f.server.ID, mode)
	if err != nil || res.OK || res.Error != "connection refused" {
		t.Fatalf("platform exit: %+v err %v", res, err)
	}
	tests := f.fake.OutboundTests()
	if sent := string(tests[len(tests)-1].Outbound); !strings.Contains(sent, "exit.example.net") {
		t.Errorf("platform exit outbound should keep its address: %s", sent)
	}
	// 用户出口同样先过地址限制
	loop := f.mustExit(t, ownLoopLink, "loop")
	_, err = f.chain.TestExitAsAdmin(ctx, loop.ID, f.server.ID, mode)
	wantErr(t, "user exit on loopback", err, ErrInvalidArgument)
	_, err = f.chain.TestExitAsAdmin(ctx, platform.ID+999, f.server.ID, mode)
	wantErr(t, "missing exit", err, ErrNotFound)
	_, err = f.chain.TestExitAsAdmin(ctx, platform.ID, f.server.ID+999, mode)
	wantErr(t, "missing server", err, ErrNotFound)
}

// HTTP / HTTPS 代理出口可用于两种链式：服务端下发 http 出站（HTTPS 带 TLS，域名固定成 IP 后留作 SNI），客户端进订阅（Ruling-51）。
func TestHTTPProxyExits(t *testing.T) {
	f := newChainApplyFixture(t)
	first, second := f.nodes[0], f.nodes[1]
	plain := f.mustExit(t, ownHTTPLink, "")
	if plain.Protocol != render.ProtoHTTP || plain.Name != "office-http" || plain.Port != 8080 {
		t.Fatalf("http exit = %+v", plain)
	}
	secure := f.mustExit(t, ownHTTPSLink, "")
	f.mustRoute(t, serverIn(ChainExitOwn, plain.ID, first.ID))
	f.mustRoute(t, serverIn(ChainExitOwn, secure.ID, second.ID))
	f.apply(t)
	tpl := f.template(t)
	for _, want := range []string{`"protocol":"http"`, `"user":"sample-user"`, `"address":"203.0.114.21"`, `"address":"` + guardPublicV4 + `"`, `"serverName":"home.example.net"`} {
		if !strings.Contains(tpl, want) {
			t.Errorf("template lacks %s", want)
		}
	}
	// 带路径的网页地址不是代理地址
	_, err := f.chain.CreateExit(context.Background(), f.principal, "https://home.example.net/index.html", "page")
	wantErr(t, "web page url as exit", err, ErrInvalidArgument)

	// 客户端链式：Mihomo 订阅里是 http 类型的落地
	f.setFlags(t, true, true)
	f.mustRoute(t, clientIn(ChainExitOwn, secure.ID, first.ID))
	body := string(f.render(t, f.token(t), render.FormatMihomo, "").Body)
	if !strings.Contains(body, "type: http") || !strings.Contains(body, "tls: true") {
		t.Errorf("mihomo output lacks the https proxy exit:\n%s", body)
	}
}
