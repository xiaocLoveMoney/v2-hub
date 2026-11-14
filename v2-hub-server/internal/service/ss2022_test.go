package service

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestSS2022KeyHelpers(t *testing.T) {
	key, err := newSS2022Key()
	if err != nil {
		t.Fatalf("newSS2022Key: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != ss2022FullKeyLen || strings.ContainsAny(key, linkUnsafeBase64) {
		t.Fatalf("key %q: %d bytes, %v", key, len(raw), err)
	}
	short, err := ss2022KeyFor(key, ss2022ShortKeyLen)
	if err != nil || strings.ContainsAny(short, linkUnsafeBase64) {
		t.Fatalf("short key %q: %v", short, err)
	}
	if b, _ := base64.StdEncoding.DecodeString(short); len(b) != ss2022ShortKeyLen || string(b) != string(raw[:ss2022ShortKeyLen]) {
		t.Fatalf("short key should be the first 16 bytes: %q", short)
	}
	if _, err := ss2022KeyFor("", ss2022FullKeyLen); !errors.Is(err, errSS2022NoKey) {
		t.Fatalf("empty key: %v", err)
	}
	if _, err := ss2022KeyFor("not-base64!!", ss2022FullKeyLen); !errors.Is(err, errSS2022BadKey) {
		t.Fatalf("bad key: %v", err)
	}

	for method, want := range map[string]int{
		"2022-blake3-aes-128-gcm": 16, "2022-blake3-aes-256-gcm": 32, "2022-blake3-chacha20-poly1305": 32, "aes-256-gcm": 0, "": 0,
	} {
		if got := ss2022KeyLen(method); got != want {
			t.Fatalf("ss2022KeyLen(%q) = %d, want %d", method, got, want)
		}
	}
	refLen := map[string]int{"1": 0, "2": 32, "3": 16, "4": 32}
	if n, err := ss2022KeyLenOf([]string{"1", "2", "4"}, refLen); n != 32 || err != nil {
		t.Fatalf("256-bit refs = %d, %v", n, err)
	}
	if n, err := ss2022KeyLenOf([]string{"1", "9"}, refLen); n != 0 || err != nil {
		t.Fatalf("no ss2022 refs = %d, %v", n, err)
	}
	if _, err := ss2022KeyLenOf([]string{"2", "3"}, refLen); !errors.Is(err, errSS2022Mixed) {
		t.Fatalf("mixed refs: %v", err)
	}
}

// 面板 password 的取值（Ruling-60）：挂了 SS2022 时写密钥（自设密码不生效），否则写自设密码（空则面板沿用 Auth）。
func TestDesiredClientPassword(t *testing.T) {
	key, err := newSS2022Key()
	if err != nil {
		t.Fatal(err)
	}
	d := desiredClient{userID: 1, email: "u1-x", cred: clientCredential{UUID: "u", Auth: "auth-token", Password: "my-Pass_word", SSKey: key}}
	spec, err := d.spec()
	if err != nil || spec.PasswordOrAuth() != "my-Pass_word" || spec.Auth != "auth-token" {
		t.Fatalf("no ss2022: %+v, %v", spec, err)
	}
	d.ssKeyLen = ss2022FullKeyLen
	if spec, err = d.spec(); err != nil || spec.PasswordOrAuth() != key || spec.Auth != "auth-token" {
		t.Fatalf("ss2022 256: %+v, %v", spec, err)
	}
	d.ssKeyLen = ss2022ShortKeyLen
	short, _ := ss2022KeyFor(key, ss2022ShortKeyLen)
	if spec, err = d.spec(); err != nil || spec.PasswordOrAuth() != short {
		t.Fatalf("ss2022 128: %+v, %v", spec, err)
	}
	d.cred.SSKey = ""
	if _, err := d.spec(); !errors.Is(err, errSS2022NoKey) {
		t.Fatalf("missing key: %v", err)
	}
}

// 老用户补发 SS2022 密钥：凭据版本 +1、下发标为待同步；已有密钥的不动，再跑一次不补。
func TestEnsureSS2022Keys(t *testing.T) {
	f := newSubFixture(t)
	ctx := context.Background()
	if n, err := f.users.EnsureSS2022Keys(ctx); err != nil || n != 0 {
		t.Fatalf("new users already have keys: %d, %v", n, err)
	}
	// 模拟 M10 之前的凭据：去掉 SS2022 密钥
	sealed, err := f.users.q.GetUserCredential(ctx, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := f.users.openCredential(f.user.ID, sealed)
	if err != nil {
		t.Fatal(err)
	}
	cred.SSKey = ""
	if sealed, err = f.users.sealCredential(f.user.ID, cred); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE end_user SET client_cred_enc = $2 WHERE id = $1", f.user.ID, sealed); err != nil {
		t.Fatal(err)
	}
	var before int32
	if err := f.pool.QueryRow(ctx, "SELECT cred_version FROM end_user WHERE id = $1", f.user.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if n, err := f.users.EnsureSS2022Keys(ctx); err != nil || n != 1 {
		t.Fatalf("fill old user: %d, %v", n, err)
	}
	var after int32
	if err := f.pool.QueryRow(ctx, "SELECT cred_version FROM end_user WHERE id = $1", f.user.ID).Scan(&after); err != nil || after != before+1 {
		t.Fatalf("cred_version %d -> %d, %v", before, after, err)
	}
	if ps, _ := f.users.ListUserProvisions(ctx, f.user.ID); len(ps) != 1 || ps[0].Status != ProvisionPending {
		t.Fatalf("provisions after fill = %+v, want pending", ps)
	}
	if n, err := f.users.EnsureSS2022Keys(ctx); err != nil || n != 0 {
		t.Fatalf("second run: %d, %v", n, err)
	}
}
