package crypto

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func testMasterKey(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, masterKeyLen))
}

func TestSealerRoundTripAndBinding(t *testing.T) {
	s, err := NewSealer(testMasterKey(7))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	plain := []byte("totp-material")
	sealed, err := s.Seal(plain, []byte("admin:1:totp"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := s.Open(sealed, []byte("admin:1:totp"))
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := s.Open(sealed, []byte("admin:2:totp")); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("wrong aad err = %v", err)
	}
	other, err := NewSealer(testMasterKey(8))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	if _, err := other.Open(sealed, []byte("admin:1:totp")); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("wrong key err = %v", err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := s.Open(sealed, []byte("admin:1:totp")); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("tampered err = %v", err)
	}
	if _, err := s.Open([]byte{1, 2}, nil); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("short err = %v", err)
	}
}

func TestNewSealerRejectsBadKey(t *testing.T) {
	for _, k := range []string{"", "not-base64!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := NewSealer(k); !errors.Is(err, ErrBadMasterKey) {
			t.Fatalf("NewSealer(%q) err = %v", k, err)
		}
	}
}
