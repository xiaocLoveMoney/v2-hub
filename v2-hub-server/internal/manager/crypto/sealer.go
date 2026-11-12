package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const masterKeyLen = 32 // AES-256

var (
	// ErrBadMasterKey 表示主密钥不是 Base64 编码的 32 字节。
	ErrBadMasterKey = errors.New("encryption key must be base64 of 32 bytes")
	// ErrCiphertext 表示密文被篡改、与关联数据不符或用错了主密钥。
	ErrCiphertext = errors.New("ciphertext cannot be opened")
)

// Sealer 用 AES-256-GCM 加解密小块敏感数据（TOTP 密钥、面板 token 等）。并发安全。
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer 由 Base64 编码的 32 字节主密钥创建 Sealer；主密钥只经环境变量注入（docs/03 §4.3）。
func NewSealer(masterKeyB64 string) (*Sealer, error) {
	key, err := base64.StdEncoding.DecodeString(masterKeyB64)
	if err != nil || len(key) != masterKeyLen {
		return nil, ErrBadMasterKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init gcm: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal 加密 plaintext，返回 nonce‖密文。aad 把密文绑定到具体用途与记录（如 "admin:3:totp"），
// 防止把一条记录的密文挪到另一条记录上仍能解开。
func (s *Sealer) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return s.aead.Seal(nonce, nonce, plaintext, aad), nil
}

// Open 解密 Seal 的输出；aad 必须与加密时一致。
func (s *Sealer) Open(sealed, aad []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, ErrCiphertext
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], aad)
	if err != nil {
		return nil, ErrCiphertext
	}
	return plain, nil
}
