package deploy

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"v2-hub-server/internal/manager/remote"
)

func TestRedact(t *testing.T) {
	const secret = "zq9XvT7mK2pLw4"
	in := strings.Join([]string{
		"downloading release",
		"Username: admin",
		"Password: hunter2",
		"API Token: abc",
		"Access URL: http://1.2.3.4:2053/x",
		"request failed with " + secret + " in the middle",
		"done",
	}, "\n")
	got := Redact(in, secret, "")
	for _, leaked := range []string{"admin", "hunter2", "abc", "1.2.3.4", secret} {
		if strings.Contains(got, leaked) {
			t.Errorf("redacted output still contains %q:\n%s", leaked, got)
		}
	}
	for _, kept := range []string{"downloading release", "request failed with " + redactedMark, "done"} {
		if !strings.Contains(got, kept) {
			t.Errorf("redacted output lost %q:\n%s", kept, got)
		}
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry(map[string]Deployer{"b": nil, "a": nil})
	if _, ok := reg.Get("a"); !ok {
		t.Fatal("registered type not found")
	}
	if _, ok := reg.Get("sui"); ok {
		t.Fatal("unregistered type must not be found")
	}
}

func TestFailureOf(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason Reason
		wantOK     bool
	}{
		{"deploy error", NewError(ReasonListenLocal, "x"), ReasonListenLocal, true},
		{"wrapped deploy error", fmt.Errorf("ctx: %w", NewError(ReasonDownloadFailed, "x")), ReasonDownloadFailed, true},
		{"ssh unreachable", &remote.Error{Kind: remote.KindUnreachable, Message: "x"}, ReasonUnreachable, true},
		{"ssh auth", &remote.Error{Kind: remote.KindAuth, Message: "x"}, ReasonAuthFailed, true},
		{"ssh host key", &remote.Error{Kind: remote.KindHostKey, Message: "x"}, ReasonHostKeyChanged, true},
		{"ssh command", &remote.Error{Kind: remote.KindCommand, Message: "x"}, ReasonCommandFailed, true},
		{"unknown ssh kind falls back", &remote.Error{Kind: "new_kind", Message: "x"}, ReasonCommandFailed, true},
		{"other error", errors.New("boom"), ReasonNone, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, _, ok := FailureOf(tt.err)
			if reason != tt.wantReason || ok != tt.wantOK {
				t.Fatalf("FailureOf = %q, %v", reason, ok)
			}
		})
	}
}

func TestAsError(t *testing.T) {
	err := NewError(ReasonListenLocal, "面板只监听本机")
	if de := AsError(err); de == nil || de.Reason != ReasonListenLocal {
		t.Fatalf("AsError = %v", de)
	}
	if AsError(nil) != nil {
		t.Fatal("nil is not a deploy error")
	}
}
