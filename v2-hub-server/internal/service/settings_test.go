package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type settingsFixture struct {
	*fixture
	settings *SettingsService
	actor    *AdminPrincipal
}

func newSettingsFixture(t *testing.T) *settingsFixture {
	t.Helper()
	f := newFixture(t)
	f.bootstrap(t, "root")
	sub := SubscriptionConfig{BaseURL: "https://user.example.com/", Title: "from-file", UpdateIntervalHours: 6, InfoNodes: true, LogRetention: 48 * time.Hour}
	return &settingsFixture{fixture: f, settings: NewSettingsService(f.pool, sub, f.clock.now), actor: f.login(t, "root").Principal}
}

func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func (f *settingsFixture) update(t *testing.T, patch map[string]any) (Settings, error) {
	t.Helper()
	return updateSettings(t, f.settings, f.actor, patch)
}

func updateSettings(t *testing.T, settings *SettingsService, actor *AdminPrincipal, patch map[string]any) (Settings, error) {
	t.Helper()
	raw := make(map[string]json.RawMessage, len(patch))
	for k, v := range patch {
		raw[k] = rawJSON(t, v)
	}
	return settings.Update(context.Background(), actor, raw, nil)
}

func TestSettingsDefaultsComeFromCodeAndConfigFile(t *testing.T) {
	f := newSettingsFixture(t)
	cur, err := f.settings.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur.Register.Mode != RegisterInvite || !cur.Register.EmailVerify || cur.Checkin.Enabled || !cur.Ticket.Enabled || cur.Shared.Enabled {
		t.Fatalf("defaults = %+v", cur)
	}
	want := SubscriptionSettings{BaseURL: "https://user.example.com", Title: "from-file", UpdateIntervalHours: 6, InfoNodes: true, LogRetentionDays: 2}
	if cur.Subscription != want {
		t.Fatalf("subscription defaults = %+v, want %+v", cur.Subscription, want)
	}
}

func TestSettingsUpdatePersistsOnlyGivenKeys(t *testing.T) {
	f := newSettingsFixture(t)
	next, err := f.update(t, map[string]any{"register.mode": RegisterOpen, "register.emailBlock": []string{"Spam.Example", "spam.example"}, "checkin.enabled": true})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if next.Register.Mode != RegisterOpen || !next.Checkin.Enabled || len(next.Register.EmailBlock) != 1 || next.Register.EmailBlock[0] != "spam.example" {
		t.Fatalf("after update = %+v", next)
	}
	// 另起一个服务实例（空缓存）读库，确认已落库且未提到的键仍是默认值
	fresh := NewSettingsService(f.pool, SubscriptionConfig{}, f.clock.now)
	cur, err := fresh.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur.Register.Mode != RegisterOpen || !cur.Checkin.Enabled || cur.Site.Name != defaultSiteName || cur.Invite.UserMaxUses != defaultInviteMaxUses {
		t.Fatalf("reloaded = %+v", cur)
	}
	var keys int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM setting").Scan(&keys); err != nil || keys != 3 {
		t.Fatalf("stored keys = %d, %v; want 3", keys, err)
	}
	var detail []byte
	if err := f.pool.QueryRow(context.Background(), "SELECT detail FROM audit_log WHERE action = $1", ActionSettingUpdate).Scan(&detail); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if !strings.Contains(string(detail), "register.mode") || strings.Contains(string(detail), "spam.example") {
		t.Fatalf("audit detail must list keys only, got %s", detail)
	}
}

func TestSettingsUpdateRejectsWholePatchOnAnyBadItem(t *testing.T) {
	f := newSettingsFixture(t)
	cases := map[string]map[string]any{
		"unknown key":          {"register.mode": RegisterOpen, "site.nope": 1},
		"bad enum":             {"site.name": "ok", "register.mode": "anyone"},
		"wrong type":           {"site.name": "ok", "checkin.enabled": "yes"},
		"out of range":         {"site.name": "ok", "mail.ratePerMinute": 0},
		"bad timezone":         {"site.name": "ok", "site.timezone": "Mars/Olympus"},
		"bad domain":           {"site.name": "ok", "register.emailAllow": []string{"not a domain"}},
		"bad base url":         {"site.name": "ok", "subscription.baseUrl": "javascript:alert(1)"},
		"bad notify email":     {"site.name": "ok", "ticket.notifyEmail": "nobody"},
		"min greater than max": {"site.name": "ok", "checkin.minMb": 600},
		"plan does not exist":  {"site.name": "ok", "register.planId": 999},
		"bad menu key":         {"site.name": "ok", "menu.user": []string{"Nodes"}},
		"duplicate menu key":   {"site.name": "ok", "menu.admin": []string{"users", "users"}},
		"null menu order":      {"site.name": "ok", "menu.user": nil},
		"too many menu keys":   {"site.name": "ok", "menu.user": manyMenuKeys(maxMenuKeys + 1)},
	}
	for name, patch := range cases {
		if _, err := f.update(t, patch); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("%s: err = %v, want invalid_argument", name, err)
		}
	}
	var keys int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM setting").Scan(&keys); err != nil || keys != 0 {
		t.Fatalf("stored keys = %d, %v; a rejected patch must save nothing", keys, err)
	}
}

func TestFlattenSettingsPatch(t *testing.T) {
	patch, err := FlattenSettingsPatch(map[string]json.RawMessage{"register": json.RawMessage(`{"mode":"open","planId":0}`)})
	if err != nil || len(patch) != 2 || string(patch["register.mode"]) != `"open"` {
		t.Fatalf("patch = %v, %v", patch, err)
	}
	if _, err := FlattenSettingsPatch(map[string]json.RawMessage{"register": json.RawMessage(`"open"`)}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("non-object group err = %v", err)
	}
}

func TestSubscriptionFollowsSettings(t *testing.T) {
	uf := newUserFixture(t)
	settings := NewSettingsService(uf.pool, SubscriptionConfig{InfoNodes: true}, uf.clock.now)
	uf.users.cfg.Settings = settings
	plan := uf.plan(t, "basic", PlanQuota{TrafficLimit: 10 * gib, DurationDays: 30})
	u := uf.newUser(t, "alice", plan.ID)
	uf.reconcile(t)
	p := &UserPrincipal{UserID: u.ID, Username: u.Username, Status: UserStatusActive}

	raw := map[string]json.RawMessage{
		"subscription.baseUrl": rawJSON(t, "https://sub.example.net"), "subscription.updateIntervalHours": rawJSON(t, 3),
		"subscription.infoNodes": rawJSON(t, false),
	}
	if _, err := settings.Update(context.Background(), uf.actor, raw, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	info, err := uf.users.GetSubscription(context.Background(), p, "https://request.example.com")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	const prefix = "https://sub.example.net/sub/"
	if len(info.URL) <= len(prefix) || info.URL[:len(prefix)] != prefix || info.UpdateIntervalHours != 3 {
		t.Fatalf("subscription info = %+v, want base from settings", info)
	}
	res, err := uf.users.RenderSubscription(context.Background(), SubscriptionRequest{Token: info.URL[len(prefix):], Format: "base64"})
	if err != nil {
		t.Fatalf("RenderSubscription: %v", err)
	}
	if got := res.Headers[headerInterval]; got != "3" {
		t.Fatalf("Profile-Update-Interval = %q, want 3", got)
	}
	if nodes := decodeBase64Sub(t, res.Body); len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 real nodes and no info nodes", len(nodes))
	}
}

func manyMenuKeys(n int) []string {
	keys := make([]string, 0, n)
	for i := range n {
		keys = append(keys, fmt.Sprintf("item-%d", i))
	}
	return keys
}

// 菜单顺序（Ruling-57）：默认空列表；保存后原样读回，空列表即恢复默认；客户门户的顺序进公开站点信息。
func TestSettingsMenuOrder(t *testing.T) {
	f := newSettingsFixture(t)
	cur, err := f.settings.Current(context.Background())
	if err != nil || cur.Menu.Admin == nil || cur.Menu.User == nil || len(cur.Menu.Admin)+len(cur.Menu.User) != 0 {
		t.Fatalf("default menu = %+v, %v", cur.Menu, err)
	}
	next, err := f.update(t, map[string]any{"menu.admin": []string{"users", "dashboard"}, "menu.user": manyMenuKeys(maxMenuKeys)})
	if err != nil || strings.Join(next.Menu.Admin, ",") != "users,dashboard" || len(next.Menu.User) != maxMenuKeys {
		t.Fatalf("updated menu = %+v, %v", next.Menu, err)
	}
	reloaded, err := f.settings.load(context.Background(), f.settings.q)
	if err != nil || strings.Join(reloaded.Menu.Admin, ",") != "users,dashboard" {
		t.Fatalf("reloaded menu = %+v, %v", reloaded.Menu, err)
	}
	reset, err := f.update(t, map[string]any{"menu.admin": []string{}})
	if err != nil || reset.Menu.Admin == nil || len(reset.Menu.Admin) != 0 {
		t.Fatalf("reset menu = %+v, %v", reset.Menu, err)
	}
}
