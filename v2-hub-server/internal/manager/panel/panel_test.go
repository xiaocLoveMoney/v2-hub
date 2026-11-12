package panel

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseBaseURL(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: "https://panel.example.com:2053/abc/", want: "https://panel.example.com:2053/abc"},
		{raw: " http://10.0.0.2:2053 ", want: "http://10.0.0.2:2053"},
		{raw: "ftp://x", wantErr: true},
		{raw: "https://x/?a=1", wantErr: true},
		{raw: "https://user:pw@x", wantErr: true},
		{raw: "panel.example.com", wantErr: true},
	}
	for _, tt := range tests {
		u, err := ParseBaseURL(tt.raw)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("ParseBaseURL(%q) want error", tt.raw)
			}
			continue
		}
		if err != nil || u.String() != tt.want {
			t.Fatalf("ParseBaseURL(%q) = %v, %v; want %s", tt.raw, u, err, tt.want)
		}
	}
}

func TestCanonicalHashIgnoresKeyOrder(t *testing.T) {
	a, err := CanonicalHash(map[string]any{"b": 1, "a": map[string]any{"y": 2, "x": 1}})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	b, err := CanonicalHash(map[string]any{"a": map[string]any{"x": 1, "y": 2}, "b": 1})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	c, err := CanonicalHash(map[string]any{"a": map[string]any{"x": 1, "y": 3}, "b": 1})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if a != b || a == c {
		t.Fatalf("hashes a=%s b=%s c=%s", a, b, c)
	}
}

func TestPinnedTLSClient(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	fp, err := CertFingerprint(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("CertFingerprint: %v", err)
	}
	sum := sha256.Sum256(srv.Certificate().Raw)
	if fp != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Fatalf("fingerprint mismatch")
	}

	get := func(cfg ConnConfig) error {
		c, err := NewHTTPClient(cfg)
		if err != nil {
			return err
		}
		resp, err := c.Get(srv.URL)
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}
	if err := get(ConnConfig{TLSMode: TLSPin, CertPinSHA256: fp}); err != nil {
		t.Fatalf("pinned request: %v", err)
	}
	wrong := base64.StdEncoding.EncodeToString(make([]byte, sha256.Size))
	if err := get(ConnConfig{TLSMode: TLSPin, CertPinSHA256: wrong}); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("wrong pin err = %v", err)
	}
	if err := get(ConnConfig{TLSMode: TLSVerify}); err == nil {
		t.Fatal("self-signed cert must fail in verify mode")
	}
	if err := get(ConnConfig{TLSMode: TLSSkip}); err != nil {
		t.Fatalf("skip mode: %v", err)
	}
	if _, err := NewHTTPClient(ConnConfig{TLSMode: TLSPin, CertPinSHA256: "bad"}); err == nil {
		t.Fatal("bad pin must be rejected")
	}
}

func TestErrorIs(t *testing.T) {
	err := error(&Error{Kind: KindAuth, Message: "token 被拒绝"})
	if !errors.Is(err, ErrAuth) || errors.Is(err, ErrUnreachable) {
		t.Fatal("errors.Is should match by kind")
	}
	if AsError(err) == nil || AsError(errors.New("x")) != nil {
		t.Fatal("AsError mismatch")
	}
}
