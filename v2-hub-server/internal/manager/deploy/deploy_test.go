package deploy

import (
	"strings"
	"testing"
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
	if got := reg.Types(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("types = %v", got)
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
