package reach

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testToken       = "test-token"
	testMeasurement = "m-1"
	finishedBody    = `{"status":"finished","results":[
		{"probe":{"country":"CN","city":"Shanghai","network":"China Telecom"},"result":{"status":"finished","stats":{"avg":31.5,"rcv":3}}},
		{"probe":{"country":"CN","city":"Wuhan","network":"China Unicom"},"result":{"status":"finished","stats":{"avg":null,"rcv":0}}},
		{"probe":{"country":"CN","city":"Beijing","network":"China Mobile"},"result":{"status":"offline"}},
		{"probe":{"country":"JP","city":"Tokyo","network":"NTT"},"result":{"status":"finished","stats":{"avg":2.1,"rcv":3}}}
	]}`
)

// fakeAPI 模拟探测服务：记下建测量的请求，前 pending 次轮询返回进行中。
type fakeAPI struct {
	mu       sync.Mutex
	created  []measurementRequest
	auth     []string
	pending  int
	createAs int
}

func (f *fakeAPI) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get(headerAuth))
	if r.Method == http.MethodPost && r.URL.Path == pathMeasurements {
		if f.createAs != 0 {
			w.WriteHeader(f.createAs)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var req measurementRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.created = append(f.created, req)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"` + testMeasurement + `","probesCount":5}`))
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == pathMeasurements+"/"+testMeasurement {
		if f.pending > 0 {
			f.pending--
			_, _ = w.Write([]byte(`{"status":"in-progress","results":[]}`))
			return
		}
		_, _ = w.Write([]byte(finishedBody))
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func newTestClient(t *testing.T, api *fakeAPI, token string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(api.handler))
	t.Cleanup(srv.Close)
	c, err := New(Config{APIURL: srv.URL + "/", Token: token, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 测试里不真的等轮询间隔
	c.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return c
}

func TestProbeTCP(t *testing.T) {
	api := &fakeAPI{pending: 2}
	c := newTestClient(t, api, testToken)
	req := Request{Address: "node.example.com", Port: 8443, Mode: ModeTCP}
	if got := c.Cost(req); got != MainlandProbes+len(controlCountries) {
		t.Fatalf("Cost = %d", got)
	}
	results, err := c.Probe(context.Background(), req)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(api.created) != 1 {
		t.Fatalf("created = %d, want 1", len(api.created))
	}
	body := api.created[0]
	if body.Type != measurementPing || body.Target != "node.example.com" || body.Options.Protocol != protocolTCP || body.Options.Port != 8443 {
		t.Fatalf("request = %+v", body)
	}
	if len(body.Locations) != 3 || body.Locations[0] != (location{Country: MainlandCountry, Limit: MainlandProbes}) {
		t.Fatalf("locations = %+v", body.Locations)
	}
	for _, a := range api.auth {
		if a != bearerPrefix+testToken {
			t.Fatalf("authorization = %q", a)
		}
	}
	if len(results) != 4 {
		t.Fatalf("results = %+v", results)
	}
	if r := results[0]; !r.IsMainland || !r.IsCounted || !r.IsOK || r.RTTMs == nil || r.City != "Shanghai" || r.Network != "China Telecom" {
		t.Fatalf("mainland ok probe = %+v", r)
	}
	if r := results[1]; !r.IsMainland || !r.IsCounted || r.IsOK || r.RTTMs != nil {
		t.Fatalf("mainland failed probe = %+v", r)
	}
	if r := results[2]; r.IsCounted {
		t.Fatalf("offline probe must not be counted: %+v", r)
	}
	if r := results[3]; r.IsMainland || !r.IsOK || r.Country != "JP" {
		t.Fatalf("control probe = %+v", r)
	}
}

func TestProbeICMPMainlandOnly(t *testing.T) {
	api := &fakeAPI{}
	c := newTestClient(t, api, "")
	req := Request{Address: "203.0.113.10", Mode: ModeICMP, MainlandOnly: true}
	if got := c.Cost(req); got != MainlandProbes {
		t.Fatalf("Cost = %d", got)
	}
	if _, err := c.Probe(context.Background(), req); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	body := api.created[0]
	if body.Options.Protocol != protocolICMP || body.Options.Port != 0 || len(body.Locations) != 1 {
		t.Fatalf("request = %+v", body)
	}
	if api.auth[0] != "" {
		t.Fatalf("no token must send no authorization, got %q", api.auth[0])
	}
}

func TestProbeErrors(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		status int
		want   error
	}{
		{"rate limited", http.StatusTooManyRequests, ErrRateLimited},
		{"rejected", http.StatusUnprocessableEntity, ErrRejected},
		{"server error", http.StatusBadGateway, ErrUnreachable},
	} {
		api := &fakeAPI{createAs: tc.status}
		c := newTestClient(t, api, "")
		if _, err := c.Probe(ctx, Request{Address: "node.example.com", Port: 443, Mode: ModeTCP}); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}

	api := &fakeAPI{}
	c := newTestClient(t, api, "")
	if _, err := c.Probe(ctx, Request{Address: "10.0.0.8", Port: 443, Mode: ModeTCP}); !errors.Is(err, ErrNotPublic) || len(api.created) != 0 {
		t.Fatalf("private target: err = %v, requests = %d", err, len(api.created))
	}
	if _, err := c.Probe(ctx, Request{Address: "node.example.com", Port: 0, Mode: ModeTCP}); !errors.Is(err, ErrRejected) {
		t.Fatalf("bad port err = %v", err)
	}
	// 一直不结束：到时限返回连不上
	stuck := &fakeAPI{pending: 1 << 20}
	sc := newTestClient(t, stuck, "")
	polls := 0
	sc.sleep = func(_ context.Context, _ time.Duration) error {
		polls++
		if polls > 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	if _, err := sc.Probe(ctx, Request{Address: "node.example.com", Port: 443, Mode: ModeTCP}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("stuck measurement err = %v", err)
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	if _, err := New(Config{APIURL: "ftp://example.com"}); !errors.Is(err, ErrBadConfig) {
		t.Fatalf("err = %v", err)
	}
	c, err := New(Config{})
	if err != nil || !strings.HasPrefix(c.base, "https://") {
		t.Fatalf("default client = %+v, %v", c, err)
	}
}

func TestIsPublicTarget(t *testing.T) {
	for address, want := range map[string]bool{
		"node.example.com": true, "203.0.113.10": true, "2001:db8::1": true,
		"": false, "localhost": false, "xui-b": false, "nas.local": false, "db.internal": false, "router.lan.": false, "127.0.0.1": false, "10.1.2.3": false, "192.168.1.1": false,
		"100.64.0.1": false, "169.254.1.1": false, "::1": false, "fd00::1": false, "0.0.0.0": false,
	} {
		if got := IsPublicTarget(address); got != want {
			t.Errorf("IsPublicTarget(%q) = %v, want %v", address, got, want)
		}
	}
}
