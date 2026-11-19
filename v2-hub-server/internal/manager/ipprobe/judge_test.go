package ipprobe

import (
	"net/netip"
	"testing"
)

func page(code, oh int) Fetch {
	return Fetch{Exit: 0, Code: code, Marks: map[string]int{"oh": oh, "uc": oh, "vpn": oh}, Grabs: map[string]string{}}
}

var failedFetch = Fetch{Exit: curlExitTimeout, Marks: map[string]int{}, Grabs: map[string]string{}}

func TestJudgeNetflix(t *testing.T) {
	cases := []struct {
		name           string
		orig, licensed Fetch
		want           string
	}{
		{"both ok", page(200, 0), page(200, 0), StatusYes},
		{"originals only", page(200, 0), page(200, 2), StatusOriginalsOnly},
		{"licensed 404", page(200, 0), page(404, 0), StatusOriginalsOnly},
		{"neither", page(200, 1), page(200, 1), StatusNo},
		{"forbidden", page(403, 0), page(403, 0), StatusNo},
		{"network", failedFetch, failedFetch, StatusFailed},
		{"half failed", page(200, 0), failedFetch, StatusUnknown},
	}
	for _, c := range cases {
		got, _ := judgeNetflix(map[string]Fetch{"nfo": c.orig, "nfl": c.licensed})
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	orig := page(200, 0)
	orig.URL = "https://www.netflix.com/hk-en/title/81280792"
	if _, region := judgeNetflix(map[string]Fetch{"nfo": orig, "nfl": page(200, 0)}); region != "HK" {
		t.Errorf("region from path = %q", region)
	}
}

func TestJudgeChatGPT(t *testing.T) {
	cases := []struct {
		web, app Fetch
		want     string
	}{
		{page(200, 0), page(200, 0), StatusYes},
		{page(200, 0), page(200, 1), StatusWebOnly},
		{page(200, 1), page(200, 0), StatusAppOnly},
		{page(200, 1), page(403, 0), StatusNo},
		{failedFetch, failedFetch, StatusFailed},
	}
	for i, c := range cases {
		trace := Fetch{Exit: 0, Code: 200, Grabs: map[string]string{"loc": "loc=US"}}
		got, region := judgeChatGPT(map[string]Fetch{"cgw": c.web, "cga": c.app, "cgt": trace})
		if got != c.want || region != "US" {
			t.Errorf("case %d: got %s/%s, want %s/US", i, got, region, c.want)
		}
	}
}

func TestJudgeYouTube(t *testing.T) {
	f := Fetch{Exit: 0, Code: 200, Marks: map[string]int{"cn": 1}, Grabs: map[string]string{}}
	if s, r := judgeYouTube(map[string]Fetch{"yt": f}); s != StatusNo || r != regionChina {
		t.Errorf("cn: %s %s", s, r)
	}
	f = Fetch{Exit: 0, Code: 200, Marks: map[string]int{"na": 1}, Grabs: map[string]string{"cr": `"contentRegion":"RU"`}}
	if s, r := judgeYouTube(map[string]Fetch{"yt": f}); s != StatusNo || r != "RU" {
		t.Errorf("na: %s %s", s, r)
	}
	f = Fetch{Exit: 0, Code: 200, Marks: map[string]int{}, Grabs: map[string]string{}}
	if s, _ := judgeYouTube(map[string]Fetch{"yt": f}); s != StatusUnknown {
		t.Errorf("no marks: %s", s)
	}
}

func TestUnlockMethod(t *testing.T) {
	public := Fetch{RemoteIP: netip.MustParseAddr("142.250.0.1")}
	cgnat := Fetch{RemoteIP: netip.MustParseAddr("100.64.1.2")}
	if unlockMethod(public, false) != MethodNative || unlockMethod(cgnat, false) != MethodDNS ||
		unlockMethod(public, true) != MethodDNS || unlockMethod(Fetch{}, false) != MethodUnknown {
		t.Error("unlockMethod table mismatch")
	}
	r := judgeMedia(mediaServices[0], map[string]Fetch{"nfo": page(200, 1), "nfl": page(200, 1)}, true)
	if r.Method != MethodUnknown {
		t.Errorf("unusable service must not report a method, got %s", r.Method)
	}
}

func TestJudgeMail(t *testing.T) {
	target := MailTarget{Key: "gmail", MX: "mx.example", Port: smtpPort}
	cases := []struct {
		exit, code string
		noTool     bool
		want       string
	}{
		{"0", "220", false, MailOK},
		{"28", "220", false, MailOK},
		{"0", "554", false, MailRejected},
		{"7", "", false, MailRefused},
		{"28", "", false, MailTimeout},
		{"124", "", false, MailTimeout},
		{"6", "", false, MailDNSFailed},
		{noToolMark, "", true, MailNoTool},
		{"", "", true, MailNoTool},
		{"56", "", false, MailFailed},
		{"0", "2x0", false, MailTimeout},
	}
	for _, c := range cases {
		kv := map[string]string{"mxgmail.exit": c.exit, "mxgmail.code": c.code}
		if got := judgeMail(target, kv, c.noTool).Result; got != c.want {
			t.Errorf("exit=%q code=%q: got %s, want %s", c.exit, c.code, got, c.want)
		}
	}
	if got := judgeMail(MailTarget{Key: "qq"}, nil, false).Result; got != MailDNSFailed {
		t.Errorf("no MX: %s", got)
	}
}

func TestPort25(t *testing.T) {
	rs := func(results ...string) []MailResult {
		out := make([]MailResult, 0, len(results))
		for _, r := range results {
			out = append(out, MailResult{Result: r})
		}
		return out
	}
	if Port25(rs(MailTimeout, MailOK)) != Port25Open ||
		Port25(rs(MailTimeout, MailRefused, MailDNSFailed)) != Port25Blocked ||
		Port25(rs(MailTimeout, MailNoTool)) != Port25Unknown ||
		Port25(rs(MailDNSFailed)) != Port25Unknown {
		t.Error("Port25 table mismatch")
	}
}
