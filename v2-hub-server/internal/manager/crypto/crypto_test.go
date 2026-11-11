package crypto

import (
	"errors"
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("HashPassword() err = %v", err)
	}
	if !strings.HasPrefix(hash, phcPrefix) {
		t.Fatalf("hash %q missing argon2id prefix", hash)
	}
	other, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("HashPassword() err = %v", err)
	}
	if hash == other {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}

	tests := []struct {
		name     string
		password string
		want     bool
	}{
		{name: "match", password: "correct horse battery", want: true},
		{name: "mismatch", password: "correct horse batterY", want: false},
		{name: "empty", password: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, err := VerifyPassword(tt.password, hash)
			if err != nil {
				t.Fatalf("VerifyPassword() err = %v", err)
			}
			if ok != tt.want {
				t.Fatalf("VerifyPassword() = %v, want %v", ok, tt.want)
			}
		})
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	for _, encoded := range []string{"", "plain", "$argon2id$v=19$m=1,t=1,p=1$!!$abc", "$bcrypt$v=19$m=1,t=1,p=1$YQ$YQ"} {
		if _, err := VerifyPassword("x", encoded); !errors.Is(err, ErrMalformedHash) {
			t.Fatalf("VerifyPassword(%q) err = %v, want ErrMalformedHash", encoded, err)
		}
	}
}

func TestRandomToken(t *testing.T) {
	const tokenBytes = 32
	a, err := RandomToken(tokenBytes)
	if err != nil {
		t.Fatalf("RandomToken() err = %v", err)
	}
	b, err := RandomToken(tokenBytes)
	if err != nil {
		t.Fatalf("RandomToken() err = %v", err)
	}
	if a == b || len(a) != 43 {
		t.Fatalf("unexpected tokens %q %q", a, b)
	}
	if len(SHA256(a)) != 32 {
		t.Fatal("SHA256 length must be 32")
	}
}
