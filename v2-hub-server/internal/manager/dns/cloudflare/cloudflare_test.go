package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"v2-hub-server/internal/manager/dns"
)

const (
	testZone   = "example.com"
	testZoneID = "zone123"
	testSecret = "cf-test-value"
)

// fakeAPI 是内存里的 Cloudflare DNS 记录接口。
type fakeAPI struct {
	mu      sync.Mutex
	records map[string]record
	nextID  int
	calls   []string
	// createErr 非 nil 时新建记录一律被拒并返回它。
	createErr *apiError
}

func newFakeAPI() *fakeAPI { return &fakeAPI{records: map[string]record{}} }

func (f *fakeAPI) write(w http.ResponseWriter, status int, result any, errs ...apiError) {
	raw, _ := json.Marshal(result)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Success: len(errs) == 0, Errors: errs, Result: raw})
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer "+testSecret {
		f.write(w, http.StatusForbidden, nil, apiError{Code: 9109, Message: "Invalid access token"})
		return
	}
	base := "/zones/" + testZoneID + "/dns_records"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/zones":
		zones := []map[string]string{}
		if r.URL.Query().Get("name") == testZone {
			zones = append(zones, map[string]string{"id": testZoneID, "name": testZone})
		}
		f.write(w, http.StatusOK, zones)
	case r.Method == http.MethodGet && r.URL.Path == base:
		out := []record{}
		for _, rec := range f.records {
			if rec.Name == r.URL.Query().Get("name") {
				out = append(out, rec)
			}
		}
		f.write(w, http.StatusOK, out)
	case r.Method == http.MethodPost && r.URL.Path == base:
		var rec record
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			f.write(w, http.StatusBadRequest, nil, apiError{Code: 1, Message: "bad json"})
			return
		}
		if f.createErr != nil {
			f.write(w, http.StatusBadRequest, nil, *f.createErr)
			return
		}
		f.nextID++
		rec.ID = "rec" + string(rune('0'+f.nextID))
		f.records[rec.ID] = rec
		f.write(w, http.StatusOK, rec)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/"):
		id := strings.TrimPrefix(r.URL.Path, base+"/")
		if _, ok := f.records[id]; !ok {
			f.write(w, http.StatusNotFound, nil, apiError{Code: 81044, Message: "Record does not exist."})
			return
		}
		delete(f.records, id)
		f.write(w, http.StatusOK, map[string]string{"id": id})
	default:
		f.write(w, http.StatusNotFound, nil, apiError{Code: 7003, Message: "no route"})
	}
}

func (f *fakeAPI) seed(rec record) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[rec.ID] = rec
}

func (f *fakeAPI) named(name string) []record {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []record
	for _, rec := range f.records {
		if rec.Name == name {
			out = append(out, rec)
		}
	}
	return out
}

func open(t *testing.T, api *fakeAPI, zone, token string) dns.Provider {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	p, err := NewFactory(Config{BaseURL: srv.URL})(dns.Account{Zone: zone, Token: token})
	if err != nil {
		t.Fatalf("open provider: %v", err)
	}
	return p
}

func TestCheckZone(t *testing.T) {
	ctx := context.Background()
	if err := open(t, newFakeAPI(), testZone, testSecret).CheckZone(ctx); err != nil {
		t.Fatalf("CheckZone: %v", err)
	}
	err := open(t, newFakeAPI(), "other.org", testSecret).CheckZone(ctx)
	if de := dns.AsError(err); de == nil || de.Kind != dns.KindZone {
		t.Fatalf("unknown zone: want KindZone, got %v", err)
	}
	err = open(t, newFakeAPI(), testZone, "wrong").CheckZone(ctx)
	de := dns.AsError(err)
	if de == nil || de.Kind != dns.KindAuth {
		t.Fatalf("bad token: want KindAuth, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestPointToCreatesByTargetKind(t *testing.T) {
	cases := []struct{ name, target, wantType, wantContent string }{
		{"v4.example.com", "203.0.113.7", "A", "203.0.113.7"},
		{"v6.example.com", "2001:db8::1", "AAAA", "2001:db8::1"},
		{"cn.example.com", "origin.example.net", "CNAME", "origin.example.net"},
	}
	api := newFakeAPI()
	p := open(t, api, testZone, testSecret)
	for _, tc := range cases {
		if err := p.PointTo(context.Background(), tc.name, tc.target); err != nil {
			t.Fatalf("PointTo %s: %v", tc.name, err)
		}
		got := api.named(tc.name)
		if len(got) != 1 || got[0].Type != tc.wantType || got[0].Content != tc.wantContent || got[0].Proxied {
			t.Fatalf("%s: got %+v", tc.name, got)
		}
	}
}

func TestPointToReplacesOwnAddressRecords(t *testing.T) {
	api := newFakeAPI()
	const name = "node.example.com"
	api.seed(record{ID: "old1", Type: "A", Name: name, Content: "198.51.100.1", Comment: recordComment})
	api.seed(record{ID: "old2", Type: "CNAME", Name: name, Content: "elsewhere.example.net", Comment: recordComment})
	api.seed(record{ID: "keep", Type: "TXT", Name: name, Content: `"note"`})
	p := open(t, api, testZone, testSecret)
	if err := p.PointTo(context.Background(), name, "203.0.113.7"); err != nil {
		t.Fatalf("PointTo: %v", err)
	}
	var address, txt int
	for _, rec := range api.named(name) {
		switch rec.Type {
		case "A":
			address++
			if rec.Content != "203.0.113.7" {
				t.Fatalf("stale A record left: %+v", rec)
			}
		case "TXT":
			txt++
		default:
			t.Fatalf("unexpected record left: %+v", rec)
		}
	}
	if address != 1 || txt != 1 {
		t.Fatalf("want one A and the untouched TXT, got A=%d TXT=%d", address, txt)
	}
}

func TestPointToIsIdempotent(t *testing.T) {
	api := newFakeAPI()
	const name = "node.example.com"
	p := open(t, api, testZone, testSecret)
	for range 2 {
		if err := p.PointTo(context.Background(), name, "203.0.113.7"); err != nil {
			t.Fatalf("PointTo: %v", err)
		}
	}
	var posts int
	for _, call := range api.calls {
		if strings.HasPrefix(call, http.MethodPost) {
			posts++
		}
	}
	if got := api.named(name); len(got) != 1 || posts != 1 {
		t.Fatalf("want one record created once, got %d records and %d posts", len(got), posts)
	}
}

func TestPointToReplacesProxiedRecord(t *testing.T) {
	api := newFakeAPI()
	const name = "node.example.com"
	api.seed(record{ID: "prox", Type: "A", Name: name, Content: "203.0.113.7", Proxied: true, Comment: recordComment})
	p := open(t, api, testZone, testSecret)
	if err := p.PointTo(context.Background(), name, "203.0.113.7"); err != nil {
		t.Fatalf("PointTo: %v", err)
	}
	if got := api.named(name); len(got) != 1 || got[0].Proxied {
		t.Fatalf("proxied record should be replaced by a dns-only one, got %+v", got)
	}
}

// 别处建的记录（不带 v2-hub 注释）不删不改：冲突就拒绝，且一条都不动（Ruling-75）。
func TestPointToRefusesConflictingForeignRecords(t *testing.T) {
	const name = "www.example.com"
	cases := map[string]record{
		"other address": {ID: "site", Type: "A", Name: name, Content: "198.51.100.9"},
		"proxied same":  {ID: "site", Type: "A", Name: name, Content: "203.0.113.7", Proxied: true},
		"cname":         {ID: "site", Type: "CNAME", Name: name, Content: "pages.example.net"},
	}
	for label, foreign := range cases {
		api := newFakeAPI()
		api.seed(foreign)
		api.seed(record{ID: "mine", Type: "A", Name: name, Content: "198.51.100.1", Comment: recordComment})
		err := open(t, api, testZone, testSecret).PointTo(context.Background(), name, "203.0.113.7")
		de := dns.AsError(err)
		if de == nil || de.Kind != dns.KindConflict || !strings.Contains(de.Message, foreign.Content) {
			t.Fatalf("%s: want KindConflict naming the record, got %v", label, err)
		}
		if got := api.named(name); len(got) != 2 {
			t.Fatalf("%s: nothing may change on conflict, got %+v", label, got)
		}
	}
}

func TestPointToKeepsCompatibleForeignRecords(t *testing.T) {
	const name = "node.example.com"
	// 别处建的、已经指向这台主机的 A：照用，不新建
	api := newFakeAPI()
	api.seed(record{ID: "manual", Type: "A", Name: name, Content: "203.0.113.7"})
	if err := open(t, api, testZone, testSecret).PointTo(context.Background(), name, "203.0.113.7"); err != nil {
		t.Fatalf("PointTo: %v", err)
	}
	if got := api.named(name); len(got) != 1 || got[0].ID != "manual" {
		t.Fatalf("a matching foreign record should be used as is, got %+v", got)
	}
	// 别处建的 AAAA 与要建的 A 可以共存：保留它，另建 A
	api = newFakeAPI()
	api.seed(record{ID: "v6", Type: "AAAA", Name: name, Content: "2001:db8::7"})
	if err := open(t, api, testZone, testSecret).PointTo(context.Background(), name, "203.0.113.7"); err != nil {
		t.Fatalf("PointTo: %v", err)
	}
	if got := api.named(name); len(got) != 2 {
		t.Fatalf("want the foreign AAAA kept next to the new A, got %+v", got)
	}
}

func TestACMEHookCarriesTokenWithoutCallingAPI(t *testing.T) {
	api := newFakeAPI()
	hook := open(t, api, testZone, testSecret).ACMEHook()
	if hook.Name != "dns_cf" || len(hook.Env) != 1 || hook.Env["CF_Token"] != testSecret {
		t.Fatalf("hook: name %q, %d env vars", hook.Name, len(hook.Env))
	}
	if len(api.calls) != 0 {
		t.Fatalf("ACMEHook must not call the API, got %v", api.calls)
	}
}

func TestRejectedCarriesProviderMessage(t *testing.T) {
	api := newFakeAPI()
	api.createErr = &apiError{Code: 81057, Message: "Record already exists."}
	err := open(t, api, testZone, testSecret).PointTo(context.Background(), "node.example.com", "203.0.113.7")
	de := dns.AsError(err)
	if de == nil || de.Kind != dns.KindRejected || !strings.Contains(de.Message, "Record already exists") {
		t.Fatalf("want KindRejected with provider message, got %v", err)
	}
}

func TestFactoryRequiresZoneAndToken(t *testing.T) {
	if _, err := NewFactory(Config{})(dns.Account{Zone: testZone}); err == nil {
		t.Fatal("want error without token")
	}
}
