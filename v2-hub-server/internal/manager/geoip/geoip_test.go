package geoip

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

const isoAssignedPlusXK = 250

func TestCountryCodeList(t *testing.T) {
	if n := len(countryCodes); n != isoAssignedPlusXK {
		t.Fatalf("country code count = %d, want %d", n, isoAssignedPlusXK)
	}
	for _, c := range []string{"US", "HK", "JP", "XK"} {
		if !IsCountryCode(c) {
			t.Errorf("IsCountryCode(%q) = false", c)
		}
	}
	for _, c := range []string{"", "us", "ZZ", "USA", "美国"} {
		if IsCountryCode(c) {
			t.Errorf("IsCountryCode(%q) = true", c)
		}
	}
}

func TestIsPublic(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8": true, "2001:4860:4860::8888": true, "::ffff:1.1.1.1": true,
		"10.0.0.1": false, "192.168.1.1": false, "127.0.0.1": false, "100.64.1.1": false, "169.254.1.1": false,
		"::1": false, "fd00::1": false, "0.0.0.0": false,
	}
	for raw, want := range cases {
		if got := IsPublic(netip.MustParseAddr(raw)); got != want {
			t.Errorf("IsPublic(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestNew(t *testing.T) {
	c, err := New(Config{})
	if c != nil || err != nil {
		t.Fatalf("empty url should disable: %v %v", c, err)
	}
	for _, bad := range []string{"https://api.country.is/", "ftp://x/{ip}", "{ip}"} {
		if _, err := New(Config{LookupURL: bad}); !errors.Is(err, ErrBadConfig) {
			t.Errorf("New(%q) err = %v, want ErrBadConfig", bad, err)
		}
	}
}

func TestCountry(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		switch {
		case strings.HasSuffix(r.URL.Path, "/1.1.1.1"):
			_, _ = w.Write([]byte(`{"ip":"1.1.1.1","country":"au"}`))
		case strings.HasSuffix(r.URL.Path, "/8.8.8.8"):
			_, _ = w.Write([]byte(`{"country_code":"US"}`))
		case strings.HasSuffix(r.URL.Path, "/9.9.9.9"):
			_, _ = w.Write([]byte(`{"country":"ZZ"}`))
		case strings.HasSuffix(r.URL.Path, "/4.4.4.4"):
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer srv.Close()
	c, err := New(Config{LookupURL: srv.URL + "/geo/{ip}"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if code, err := c.Country(ctx, netip.MustParseAddr("1.1.1.1")); err != nil || code != "AU" || gotPath != "/geo/1.1.1.1" {
		t.Fatalf("1.1.1.1 → %q %v (path %s)", code, err, gotPath)
	}
	if code, err := c.Country(ctx, netip.MustParseAddr("8.8.8.8")); err != nil || code != "US" {
		t.Fatalf("8.8.8.8 → %q %v", code, err)
	}
	if _, err := c.Country(ctx, netip.MustParseAddr("9.9.9.9")); !errors.Is(err, ErrUnknown) {
		t.Fatalf("invalid code err = %v", err)
	}
	if _, err := c.Country(ctx, netip.MustParseAddr("4.4.4.4")); !errors.Is(err, ErrUnknown) {
		t.Fatalf("404 err = %v", err)
	}
	if _, err := c.Country(ctx, netip.MustParseAddr("5.5.5.5")); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("429 err = %v", err)
	}
	gotPath = ""
	if _, err := c.Country(ctx, netip.MustParseAddr("10.1.2.3")); !errors.Is(err, ErrNotPublic) || gotPath != "" {
		t.Fatalf("private err = %v, path %q", err, gotPath)
	}
}
