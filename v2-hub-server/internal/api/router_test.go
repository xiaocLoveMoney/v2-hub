package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testPingTimeout = time.Second

type fakePinger struct {
	err error
}

func (f fakePinger) Ping(context.Context) error { return f.err }

func newTestRouter(dbErr error) http.Handler {
	return NewRouter(Deps{Database: fakePinger{err: dbErr}, PingTimeout: testPingTimeout})
}

func decodeStatus(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body statusResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body.Status
}

func TestProbes(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		dbErr      error
		wantCode   int
		wantStatus string
	}{
		{name: "healthz ok", method: http.MethodGet, path: "/healthz", wantCode: http.StatusOK, wantStatus: statusOK},
		{name: "healthz ignores db", method: http.MethodGet, path: "/healthz", dbErr: errors.New("down"), wantCode: http.StatusOK, wantStatus: statusOK},
		{name: "readyz ok", method: http.MethodGet, path: "/readyz", wantCode: http.StatusOK, wantStatus: statusOK},
		{name: "readyz db down", method: http.MethodGet, path: "/readyz", dbErr: errors.New("down"), wantCode: http.StatusServiceUnavailable, wantStatus: statusUnavailable},
		{name: "healthz rejects post", method: http.MethodPost, path: "/healthz", wantCode: http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newTestRouter(tt.dbErr).ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantStatus != "" {
				if got := decodeStatus(t, rec); got != tt.wantStatus {
					t.Fatalf("status field = %q, want %q", got, tt.wantStatus)
				}
			}
		})
	}
}

func TestReadyzRespectsTimeout(t *testing.T) {
	blocking := pingerFunc(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	handler := NewRouter(Deps{Database: blocking, PingTimeout: 10 * time.Millisecond})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

type pingerFunc func(ctx context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }
