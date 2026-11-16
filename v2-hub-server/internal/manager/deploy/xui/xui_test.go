package xui

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/remote"
	"v2-hub-server/internal/manager/remote/remotetest"
)

const (
	fakeToken    = "tok0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"
	contractPath = "../../../../contract/upstream/3x-ui"
)

var ctx = context.Background()

func selfSignedPEM(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "panel"}, NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	sum := sha256.Sum256(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), base64.StdEncoding.EncodeToString(sum[:])
}

func out(s string) (remote.Result, error) { return remote.Result{Output: s}, nil }

func exit(code int, s string) (remote.Result, error) {
	return remote.Result{Output: s, ExitCode: code}, nil
}

func TestClassify(t *testing.T) {
	standard := map[string]string{"os_id": "debian", "arch": "x86_64", "systemd": "1", "bin": "1", "unit": "1", "version": "3.11.0", "docker": "0", "proc": "1"}
	with := func(over map[string]string) map[string]string {
		kv := make(map[string]string, len(standard))
		for k, v := range standard {
			kv[k] = v
		}
		for k, v := range over {
			kv[k] = v
		}
		return kv
	}
	tests := []struct {
		name       string
		kv         map[string]string
		privileged bool
		wantState  deploy.PanelState
		wantPlan   deploy.Plan
		wantReason deploy.Reason
	}{
		{name: "standard install is adopted", kv: standard, privileged: true, wantState: deploy.PanelStandard, wantPlan: deploy.PlanAdopt},
		{name: "clean host is installed", kv: with(map[string]string{"bin": "0", "unit": "0", "proc": "0", "version": ""}), privileged: true, wantState: deploy.PanelNone, wantPlan: deploy.PlanInstall},
		{name: "no privilege wins over everything", kv: standard, privileged: false, wantState: deploy.PanelStandard, wantReason: deploy.ReasonNoRoot},
		{name: "no systemd", kv: with(map[string]string{"systemd": "0", "os_id": "alpine"}), privileged: true, wantState: deploy.PanelStandard, wantReason: deploy.ReasonNoSystemd},
		{name: "unknown distro", kv: with(map[string]string{"os_id": "gentoo"}), privileged: true, wantState: deploy.PanelStandard, wantReason: deploy.ReasonUnsupportedOS},
		{name: "docker panel is not adopted", kv: with(map[string]string{"bin": "0", "unit": "0", "proc": "0", "docker": "1"}), privileged: true, wantState: deploy.PanelNonstandard, wantReason: deploy.ReasonNonstandard},
		{name: "binary without unit", kv: with(map[string]string{"unit": "0"}), privileged: true, wantState: deploy.PanelNonstandard, wantReason: deploy.ReasonNonstandard},
		{name: "stray process", kv: with(map[string]string{"bin": "0", "unit": "0"}), privileged: true, wantState: deploy.PanelNonstandard, wantReason: deploy.ReasonNonstandard},
		{name: "old major version", kv: with(map[string]string{"version": "2.8.5"}), privileged: true, wantState: deploy.PanelStandard, wantReason: deploy.ReasonVersionTooOld},
		{name: "unreadable version", kv: with(map[string]string{"version": ""}), privileged: true, wantState: deploy.PanelStandard, wantReason: deploy.ReasonVersionTooOld},
		{name: "v prefix accepted", kv: with(map[string]string{"version": "v3.0.1"}), privileged: true, wantState: deploy.PanelStandard, wantPlan: deploy.PlanAdopt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify(tt.kv, tt.privileged)
			if got.PanelState != tt.wantState || got.Plan != tt.wantPlan || got.Reason != tt.wantReason {
				t.Fatalf("got state=%q plan=%q reason=%q", got.PanelState, got.Plan, got.Reason)
			}
			// 不能部署时必须带说明，能部署时不带
			if (got.Reason != deploy.ReasonNone) != (got.Message != "") {
				t.Fatalf("reason %q with message %q", got.Reason, got.Message)
			}
		})
	}
}

func TestDetectParsesOutput(t *testing.T) {
	r := &remotetest.Runner{Privileged: true, Handle: func(string) (remote.Result, error) {
		return out("os_id=ubuntu\narch=aarch64\nsystemd=1\nbin=1\nversion=3.11.0\nunit=1\ndocker=0\nproc=1\n")
	}}
	det, err := New().Detect(ctx, r)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	want := deploy.Detection{OSID: "ubuntu", Arch: "aarch64", IsPrivileged: true, PanelState: deploy.PanelStandard, PanelVersion: "3.11.0", Plan: deploy.PlanAdopt}
	if det != want {
		t.Fatalf("detection = %+v", det)
	}
}

func TestAccess(t *testing.T) {
	certPEM, pin := selfSignedPEM(t)
	tests := []struct {
		name       string
		settings   string
		wantAccess deploy.PanelAccess
		wantReason deploy.Reason
	}{
		{name: "http panel", settings: "port=2053\npath=/abc/\ncert=\nlisten=\n", wantAccess: deploy.PanelAccess{Port: 2053, BasePath: "/abc/"}},
		{name: "root path", settings: "port=8443\npath=/\ncert=\nlisten=0.0.0.0\n", wantAccess: deploy.PanelAccess{Port: 8443, BasePath: "/"}},
		{name: "path without slashes", settings: "port=8443\npath=secret\n", wantAccess: deploy.PanelAccess{Port: 8443, BasePath: "/secret/"}},
		{name: "https panel", settings: "port=2053\npath=/x/\ncert=/root/cert/it's.crt\n", wantAccess: deploy.PanelAccess{HasTLS: true, Port: 2053, BasePath: "/x/", CertSHA256: pin}},
		{name: "loopback only", settings: "port=2053\npath=/\nlisten=127.0.0.1\n", wantReason: deploy.ReasonListenLocal},
		{name: "ipv6 loopback", settings: "port=2053\npath=/\nlisten=::1\n", wantReason: deploy.ReasonListenLocal},
		{name: "no port", settings: "path=/\n", wantReason: deploy.ReasonCommandFailed},
		{name: "port out of range", settings: "port=70000\npath=/\n", wantReason: deploy.ReasonCommandFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &remotetest.Runner{Privileged: true, Handle: func(script string) (remote.Result, error) {
				if strings.HasPrefix(script, "cat ") {
					return out(certPEM)
				}
				return out(tt.settings)
			}}
			got, err := New().Access(ctx, r)
			if tt.wantReason != "" {
				if de := deploy.AsError(err); de == nil || de.Reason != tt.wantReason {
					t.Fatalf("err = %v, want reason %s", err, tt.wantReason)
				}
				return
			}
			if err != nil || got != tt.wantAccess {
				t.Fatalf("access = %+v, err = %v", got, err)
			}
			if tt.wantAccess.HasTLS {
				// 证书路径来自远端，必须整体转义后才进命令行
				scripts := r.Scripts()
				if last := scripts[len(scripts)-1]; last != "cat '/root/cert/it'\\''s.crt'\n" {
					t.Fatalf("cat script = %q", last)
				}
			}
		})
	}
}

func TestIssueToken(t *testing.T) {
	rotated := "There are 2 API token(s) configured. Existing tokens cannot be retrieved in plaintext because only hashes are stored.\n" +
		"\nThe API token \"v2-hub\" has been regenerated (any previous one is now invalid):\napiToken: " + fakeToken + "\n"
	r := &remotetest.Runner{Handle: func(string) (remote.Result, error) { return out(rotated) }}
	got, err := New().IssueToken(ctx, r)
	if err != nil || got != fakeToken {
		t.Fatalf("token = %q, err = %v", got, err)
	}
	script := r.Scripts()[0]
	if !strings.Contains(script, "-tokenName v2-hub -tokenScope admin -getApiToken 2>&1") {
		t.Fatalf("script = %q", script)
	}

	old := &remotetest.Runner{Handle: func(string) (remote.Result, error) {
		return out("flag provided but not defined: -getApiToken\n")
	}}
	_, err = New().IssueToken(ctx, old)
	if de := deploy.AsError(err); de == nil || de.Reason != deploy.ReasonCommandFailed {
		t.Fatalf("err = %v", err)
	}
}

func TestInstall(t *testing.T) {
	var logged []string
	log := func(line string) { logged = append(logged, line) }

	t.Run("success uploads the pinned script and logs nothing", func(t *testing.T) {
		logged = nil
		r := &remotetest.Runner{Handle: func(string) (remote.Result, error) {
			return out("Username: abc\nPassword: def\nAPI Token: " + fakeToken + "\n")
		}}
		if err := New().Install(ctx, r, 34567, log); err != nil {
			t.Fatalf("install: %v", err)
		}
		if ups := r.Uploads(); len(ups) != 1 || !bytes.Equal(ups[0], installScript) {
			t.Fatal("the embedded install script should be uploaded as is")
		}
		script := r.Scripts()[0]
		for _, want := range []string{
			"XUI_NONINTERACTIVE=1 XUI_SSL_MODE=none XUI_DB_TYPE=sqlite XUI_PANEL_PORT=34567 bash '" + remotetest.UploadPath + "' " + BaselineTag + " </dev/null",
			"rm -f \"$log\" '" + remotetest.UploadPath + "'",
		} {
			if !strings.Contains(script, want) {
				t.Fatalf("script lacks %q:\n%s", want, script)
			}
		}
		if len(logged) != 0 {
			t.Fatalf("success must not log installer output, got %q", logged)
		}
	})

	t.Run("failure logs redacted tail", func(t *testing.T) {
		logged = nil
		r := &remotetest.Runner{Handle: func(string) (remote.Result, error) {
			return exit(1, "step one\nPassword: hunter2\nAPI Token: "+fakeToken+"\nboom\n")
		}}
		err := New().Install(ctx, r, 0, log)
		if de := deploy.AsError(err); de == nil || de.Reason != deploy.ReasonCommandFailed {
			t.Fatalf("err = %v", err)
		}
		joined := strings.Join(logged, "\n")
		if strings.Contains(joined, "hunter2") || strings.Contains(joined, fakeToken) || !strings.Contains(joined, "boom") {
			t.Fatalf("log = %q", joined)
		}
		if strings.Contains(r.Scripts()[0], "XUI_PANEL_PORT") {
			t.Fatal("port 0 means random: no XUI_PANEL_PORT")
		}
	})

	t.Run("download failure", func(t *testing.T) {
		r := &remotetest.Runner{Handle: func(string) (remote.Result, error) {
			return exit(1, "ERROR: Failed to download x-ui, please be sure that your server can access GitHub\n")
		}}
		err := New().Install(ctx, r, 0, log)
		if de := deploy.AsError(err); de == nil || de.Reason != deploy.ReasonDownloadFailed {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("bad port never reaches the host", func(t *testing.T) {
		r := &remotetest.Runner{}
		if err := New().Install(ctx, r, 70000, log); err == nil {
			t.Fatal("want error")
		}
		if len(r.Scripts()) != 0 || len(r.Uploads()) != 0 {
			t.Fatal("nothing should be sent")
		}
	})
}

// provisionRunner 模拟一台主机：按脚本的特征给出各步的输出，并记下跑过哪些步骤。
func provisionRunner(certPEM, settings string) (*remotetest.Runner, *[]string) {
	steps := []string{}
	r := &remotetest.Runner{Privileged: true}
	r.Handle = func(script string) (remote.Result, error) {
		switch {
		case strings.Contains(script, "XUI_NONINTERACTIVE"):
			steps = append(steps, "install")
		case strings.Contains(script, "openssl req"):
			steps = append(steps, "tls")
			settings += "cert=/etc/x-ui/hub-tls/panel.crt\n"
			return out(certPEM)
		case strings.HasPrefix(script, "cat "):
			return out(certPEM)
		case strings.Contains(script, "-getListen"):
			steps = append(steps, "access")
			return out(settings)
		case strings.Contains(script, "ufw"):
			steps = append(steps, "firewall")
		case strings.Contains(script, "-getApiToken"):
			steps = append(steps, "token")
			return out("apiToken: " + fakeToken + "\n")
		}
		return out("")
	}
	return r, &steps
}

func TestProvision(t *testing.T) {
	certPEM, pin := selfSignedPEM(t)
	const settings = "port=2053\npath=/abc/\n"
	log := func(string) {}
	opts := deploy.ProvisionOptions{Address: "203.0.113.7", PanelPort: 2053}

	t.Run("install configures TLS before reading access, then opens the firewall", func(t *testing.T) {
		r, steps := provisionRunner(certPEM, settings)
		got, err := New().Provision(ctx, r, deploy.Detection{Plan: deploy.PlanInstall}, opts, log)
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		if strings.Join(*steps, ",") != "install,tls,access,firewall,token" {
			t.Fatalf("steps = %v", *steps)
		}
		want := deploy.Provisioned{Access: deploy.PanelAccess{HasTLS: true, Port: 2053, BasePath: "/abc/", CertSHA256: pin}, Token: fakeToken, IsFresh: true}
		if got != want {
			t.Fatalf("provisioned = %+v", got)
		}
	})

	t.Run("adopt only reads access and issues a token", func(t *testing.T) {
		r, steps := provisionRunner(certPEM, settings)
		got, err := New().Provision(ctx, r, deploy.Detection{Plan: deploy.PlanAdopt}, opts, log)
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		if strings.Join(*steps, ",") != "access,token" || len(r.Uploads()) != 0 {
			t.Fatalf("adopt must not install, touch TLS or the firewall; steps = %v", *steps)
		}
		want := deploy.Provisioned{Access: deploy.PanelAccess{Port: 2053, BasePath: "/abc/"}, Token: fakeToken}
		if got != want {
			t.Fatalf("provisioned = %+v", got)
		}
	})

	t.Run("a panel listening on loopback is not adopted", func(t *testing.T) {
		r, steps := provisionRunner(certPEM, settings+"listen=127.0.0.1\n")
		_, err := New().Provision(ctx, r, deploy.Detection{Plan: deploy.PlanAdopt}, opts, log)
		if de := deploy.AsError(err); de == nil || de.Reason != deploy.ReasonListenLocal {
			t.Fatalf("err = %v", err)
		}
		if strings.Join(*steps, ",") != "access" {
			t.Fatalf("no token should be issued for an unreachable panel; steps = %v", *steps)
		}
	})
}

func TestUpgradePinsBaseline(t *testing.T) {
	r := &remotetest.Runner{}
	if err := New().Upgrade(ctx, r, func(string) {}); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if ups := r.Uploads(); len(ups) != 1 || !bytes.Equal(ups[0], updateScript) {
		t.Fatal("the embedded update script should be uploaded")
	}
	scripts := r.Scripts()
	if !strings.Contains(scripts[0], "XUI_UPDATE_TAG="+BaselineTag+" bash ") || scripts[1] != isActiveScript {
		t.Fatalf("scripts = %q", scripts)
	}
}

func TestEnableTLS(t *testing.T) {
	certPEM, pin := selfSignedPEM(t)
	tests := []struct {
		address string
		wantSAN string
		wantErr bool
	}{
		{address: "203.0.113.7", wantSAN: "'subjectAltName=IP:203.0.113.7'"},
		{address: "2001:db8::1", wantSAN: "'subjectAltName=IP:2001:db8::1'"},
		{address: "panel.example.com", wantSAN: "'subjectAltName=DNS:panel.example.com'"},
		{address: "x'; rm -rf /; '", wantErr: true},
		{address: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			r := &remotetest.Runner{Handle: func(string) (remote.Result, error) { return out(certPEM) }}
			got, err := New().EnableTLS(ctx, r, tt.address)
			if tt.wantErr {
				if err == nil || len(r.Scripts()) != 0 {
					t.Fatalf("bad address must be rejected before any command; err = %v", err)
				}
				return
			}
			if err != nil || got != pin {
				t.Fatalf("pin = %q, err = %v", got, err)
			}
			if script := r.Scripts()[0]; !strings.Contains(script, "-addext "+tt.wantSAN) || !strings.Contains(script, "-subj '/CN="+tt.address+"'") {
				t.Fatalf("script = %s", script)
			}
		})
	}
}

func TestResetPanelLogin(t *testing.T) {
	r := &remotetest.Runner{}
	login, err := New().ResetPanelLogin(ctx, r)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if len(login.Username) != loginUsernameLen || len(login.Password) != loginPasswordLen {
		t.Fatalf("login = %+v", login)
	}
	if !strings.Contains(r.Scripts()[0], "-username '"+login.Username+"' -password '"+login.Password+"'") {
		t.Fatalf("script = %q", r.Scripts()[0])
	}

	failing := &remotetest.Runner{Handle: func(string) (remote.Result, error) { return exit(1, "") }}
	if _, err := New().ResetPanelLogin(ctx, failing); err == nil {
		t.Fatal("want error when the panel does not confirm")
	}
}

func TestServiceActions(t *testing.T) {
	d := New()
	r := &remotetest.Runner{}
	if err := d.RestartPanel(ctx, r); err != nil {
		t.Fatalf("restart panel: %v", err)
	}
	if err := d.RestartCore(ctx, r); err != nil {
		t.Fatalf("restart core: %v", err)
	}
	if err := d.OpenFirewall(ctx, r, 2053); err != nil {
		t.Fatalf("firewall: %v", err)
	}
	if err := d.OpenFirewall(ctx, r, 0); err == nil {
		t.Fatal("port 0 must be rejected")
	}
	down := &remotetest.Runner{Handle: func(string) (remote.Result, error) { return exit(3, "") }}
	if de := deploy.AsError(d.RestartPanel(ctx, down)); de == nil || de.Reason != deploy.ReasonCommandFailed {
		t.Fatal("non-zero exit must fail the restart")
	}
}

func TestDiagnoseRedacts(t *testing.T) {
	r := &remotetest.Runner{Handle: func(string) (remote.Result, error) {
		return out("服务状态: active\nlogin ok, token=" + fakeToken + "\nxray started\n")
	}}
	got, err := New().Diagnose(ctx, r)
	if err != nil || strings.Contains(got, fakeToken) || !strings.Contains(got, "xray started") {
		t.Fatalf("diagnose = %q, err = %v", got, err)
	}
}

// 内嵌的脚本必须与契约目录里的快照逐字节一致；改契约后要重新 `make gen-upstream`。
func TestEmbeddedScriptsMatchContract(t *testing.T) {
	files := map[string][]byte{"install-" + BaselineTag + ".sh": installScript, "update-" + BaselineTag + ".sh": updateScript}
	for name, embedded := range files {
		want, err := os.ReadFile(filepath.Join(contractPath, name))
		if err != nil {
			t.Skipf("contract snapshot not readable: %v", err)
		}
		if !bytes.Equal(want, embedded) {
			t.Errorf("%s differs from the contract snapshot; run make gen-upstream", name)
		}
	}
}

func TestInstallCert(t *testing.T) {
	certPEM, keyPEM := []byte("CERT-CHAIN"), []byte("KEY-MATERIAL")
	r := &remotetest.Runner{}
	if err := New().InstallCert(ctx, r, certPEM, keyPEM); err != nil {
		t.Fatalf("InstallCert: %v", err)
	}
	uploads := r.Uploads()
	if len(uploads) != 2 || !bytes.Equal(uploads[0], certPEM) || !bytes.Equal(uploads[1], keyPEM) {
		t.Fatalf("want the certificate then the key uploaded, got %d uploads", len(uploads))
	}
	scripts := r.Scripts()
	if len(scripts) != 1 {
		t.Fatalf("want one script, got %d", len(scripts))
	}
	script := scripts[0]
	for _, want := range []string{
		"trap 'rm -f '" + remotetest.UploadPath, `-webCert "$d/panel.crt" -webCertKey "$d/panel.key"`, "systemctl restart x-ui", `install -m 600`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script misses %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, string(keyPEM)) || strings.Contains(script, string(certPEM)) {
		t.Fatal("certificate material must travel over stdin, not the command line")
	}

	failing := &remotetest.Runner{Handle: func(string) (remote.Result, error) { return exit(1, "") }}
	err := New().InstallCert(ctx, failing, certPEM, keyPEM)
	if de := deploy.AsError(err); de == nil || de.Reason != deploy.ReasonCommandFailed {
		t.Fatalf("want command_failed, got %v", err)
	}
}
