package xui

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
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
	acmeContractPath = "../../../../contract/upstream/acme.sh"
	testCertDomain   = "node-a.example.com"
	testDirectory    = "https://acme-staging-v02.api.letsencrypt.org/directory"
	testCFValue      = "cf-test-value-0123456789"
)

func testCertRequest() deploy.CertRequest {
	return deploy.CertRequest{Domain: testCertDomain, DirectoryURL: testDirectory, DNSHook: "dns_cf", Env: map[string]string{"CF_Token": testCFValue}}
}

func TestACMESnapshotsMatchContract(t *testing.T) {
	files := map[string][]byte{"acme-3.1.6.sh": acmeScript, "dns_cf-3.1.6.sh": dnsCFHook}
	for name, embedded := range files {
		want, err := os.ReadFile(filepath.Join(acmeContractPath, name))
		if err != nil {
			t.Skipf("contract snapshot not readable: %v", err)
		}
		if !bytes.Equal(want, embedded) {
			t.Errorf("%s differs from the contract snapshot; run make gen-upstream", name)
		}
	}
}

func TestIssueCertUploadsToolsAndKeepsSecretOffCommandLine(t *testing.T) {
	r := &remotetest.Runner{Handle: func(string) (remote.Result, error) {
		return out("[Wed Nov 18 07:56:34 AM UTC 2026] Cert success.\n[Wed Nov 18 07:56:35 AM UTC 2026] Next renewal time picked from ARI window\n")
	}}
	var logged []string
	reused, err := New().IssueCert(ctx, r, testCertRequest(), func(line string) { logged = append(logged, line) })
	if err != nil || reused {
		t.Fatalf("IssueCert: reused %v, err %v", reused, err)
	}
	uploads := r.Uploads()
	if len(uploads) != 3 || !bytes.Equal(uploads[0], acmeScript) || !bytes.Equal(uploads[1], dnsCFHook) {
		t.Fatalf("want acme.sh, the hook and the env file uploaded in order, got %d uploads", len(uploads))
	}
	if env := string(uploads[2]); env != "CF_Token='"+testCFValue+"'\n" {
		t.Fatalf("env file: %q", env)
	}
	scripts := r.Scripts()
	if len(scripts) != 1 {
		t.Fatalf("want one script, got %d", len(scripts))
	}
	script := scripts[0]
	for _, want := range []string{
		"d='" + testCertDomain + "'", "server='" + testDirectory + "'", "hook='dns_cf'", "envf='" + remotetest.UploadPath + "'",
		`sed -i '/^SAVED_CF_Token=/d;/^CF_Token=/d'`, `trap 'rm -f "$envf" "$acme" "$hookf"; scrub' EXIT`, "--keylength ec-256",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script misses %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, testCFValue) {
		t.Fatal("the dns credential must travel in the uploaded file, not the command line")
	}
	if len(logged) != 2 || logged[0] != "Cert success." {
		t.Fatalf("logged lines should drop the timestamp: %q", logged)
	}
}

func TestIssueCertOutcomes(t *testing.T) {
	reusedRunner := &remotetest.Runner{Handle: func(string) (remote.Result, error) { return exit(acmeExitReused, "[t] Skipping.\n") }}
	if reused, err := New().IssueCert(ctx, reusedRunner, testCertRequest(), func(string) {}); err != nil || !reused {
		t.Fatalf("exit 2 means reused: reused %v, err %v", reused, err)
	}
	failing := &remotetest.Runner{Handle: func(string) (remote.Result, error) {
		return exit(1, "-----BEGIN CERTIFICATE-----\n[t] Adding TXT record\n[t] invalid domain\n[t] Error add txt for domain:_acme-challenge.node-a.example.com\n")
	}}
	_, err := New().IssueCert(ctx, failing, testCertRequest(), func(string) {})
	de := deploy.AsError(err)
	if de == nil || de.Reason != deploy.ReasonCertFailed || !strings.Contains(de.Message, "invalid domain；Error add txt") || strings.Contains(de.Message, "BEGIN") {
		t.Fatalf("want cert_failed with the last acme.sh lines, got %v", err)
	}
}

func TestIssueCertRejectsBadRequests(t *testing.T) {
	bad := map[string]func(*deploy.CertRequest){
		"unknown hook":   func(r *deploy.CertRequest) { r.DNSHook = "dns_unknown" },
		"upper domain":   func(r *deploy.CertRequest) { r.Domain = "Node.example.com" },
		"ip domain":      func(r *deploy.CertRequest) { r.Domain = "203.0.113.7" },
		"wildcard":       func(r *deploy.CertRequest) { r.Domain = "*.example.com" },
		"single label":   func(r *deploy.CertRequest) { r.Domain = "localhost" },
		"quoted url":     func(r *deploy.CertRequest) { r.DirectoryURL = "https://ca.example.com/'x" },
		"no env":         func(r *deploy.CertRequest) { r.Env = nil },
		"bad env name":   func(r *deploy.CertRequest) { r.Env = map[string]string{"cf;token": "v"} },
		"newline value":  func(r *deploy.CertRequest) { r.Env = map[string]string{"CF_Token": "a\nb"} },
		"empty value":    func(r *deploy.CertRequest) { r.Env = map[string]string{"CF_Token": ""} },
		"not a url":      func(r *deploy.CertRequest) { r.DirectoryURL = "letsencrypt" },
		"empty hostname": func(r *deploy.CertRequest) { r.Domain = "" },
	}
	for name, mutate := range bad {
		req := testCertRequest()
		mutate(&req)
		r := &remotetest.Runner{}
		if _, err := New().IssueCert(ctx, r, req, func(string) {}); err == nil {
			t.Errorf("%s: want error", name)
		}
		if len(r.Uploads())+len(r.Scripts()) != 0 {
			t.Errorf("%s: nothing should reach the host", name)
		}
	}
}

func leafPEM(t *testing.T, notAfter time.Time) (string, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: testCertDomain}, DNSNames: []string{testCertDomain}, NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), der
}

func TestInstallCertParsesChainAndRenewTime(t *testing.T) {
	notAfter := time.Date(2027, 1, 21, 6, 58, 0, 0, time.UTC)
	chain, der := leafPEM(t, notAfter)
	r := &remotetest.Runner{Handle: func(string) (remote.Result, error) { return out("changed=1\nrenew=1798634424\n" + chain + chain) }}
	got, err := New().InstallCert(ctx, r, testCertDomain)
	if err != nil {
		t.Fatalf("InstallCert: %v", err)
	}
	if !got.Changed || !got.NotAfter.Equal(notAfter) || !bytes.Equal(got.LeafDER, der) || got.RenewAt.Unix() != 1798634424 {
		t.Fatalf("installed: %+v", got)
	}
	script := r.Scripts()[0]
	for _, want := range []string{"d='" + testCertDomain + "'", `c="/root/cert/$d"`, `-webCert "$c/fullchain.pem" -webCertKey "$c/privkey.pem"`, "systemctl is-active --quiet x-ui"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script misses %q:\n%s", want, script)
		}
	}

	unchanged := &remotetest.Runner{Handle: func(string) (remote.Result, error) { return out("changed=0\nrenew=\n" + chain) }}
	got, err = New().InstallCert(ctx, unchanged, testCertDomain)
	if err != nil || got.Changed || !got.RenewAt.IsZero() {
		t.Fatalf("unchanged: %+v, %v", got, err)
	}
}

func TestInstallCertFailures(t *testing.T) {
	cases := map[string]struct {
		res    remote.Result
		reason deploy.Reason
	}{
		"no issued cert": {remote.Result{ExitCode: installExitNoCert}, deploy.ReasonCertFailed},
		"panel down":     {remote.Result{ExitCode: installExitDown}, deploy.ReasonCommandFailed},
		"not set":        {remote.Result{ExitCode: 4}, deploy.ReasonCommandFailed},
		"no chain":       {remote.Result{Output: "changed=1\nrenew=1\n"}, deploy.ReasonCommandFailed},
	}
	for name, c := range cases {
		r := &remotetest.Runner{Handle: func(string) (remote.Result, error) { return c.res, nil }}
		_, err := New().InstallCert(ctx, r, testCertDomain)
		if de := deploy.AsError(err); de == nil || de.Reason != c.reason {
			t.Errorf("%s: want %s, got %v", name, c.reason, err)
		}
	}
	r := &remotetest.Runner{}
	if _, err := New().InstallCert(ctx, r, "bad domain"); err == nil || len(r.Scripts()) != 0 {
		t.Fatal("an invalid domain must not reach the host")
	}
}
