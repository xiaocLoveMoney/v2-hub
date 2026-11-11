package totp

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 附录 B 的 SHA1 测试向量（密钥 "12345678901234567890"），取 8 位结果的后 6 位。
func TestCodeAtRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	vectors := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, v := range vectors {
		if got := CodeAt(secret, Step(time.Unix(v.unix, 0))); got != v.want {
			t.Fatalf("CodeAt(%d) = %s, want %s", v.unix, got, v.want)
		}
	}
}

func TestVerifyWindowAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0)
	cur := Step(now)

	tests := []struct {
		name     string
		code     string
		lastStep int64
		wantOK   bool
		wantStep int64
	}{
		{"current step", CodeAt(secret, cur), 0, true, cur},
		{"previous step within skew", CodeAt(secret, cur-1), 0, true, cur - 1},
		{"next step within skew", CodeAt(secret, cur+1), 0, true, cur + 1},
		{"outside skew", CodeAt(secret, cur-2), 0, false, 0},
		{"replay of used step", CodeAt(secret, cur), cur, false, 0},
		{"wrong length", "12345", 0, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step, ok := Verify(secret, tt.code, now, tt.lastStep)
			if ok != tt.wantOK || step != tt.wantStep {
				t.Fatalf("Verify = %d, %v; want %d, %v", step, ok, tt.wantStep, tt.wantOK)
			}
		})
	}
}

func TestGenerateSecretAndURI(t *testing.T) {
	raw, text, err := GenerateSecret()
	if err != nil || len(raw) != SecretBytes || len(text) != 32 {
		t.Fatalf("GenerateSecret = %d bytes, %q, %v", len(raw), text, err)
	}
	uri := URI("v2-hub", "root", text)
	for _, want := range []string{"otpauth://totp/v2-hub:root?", "secret=" + text, "issuer=v2-hub", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Fatalf("URI %q missing %q", uri, want)
		}
	}
}
