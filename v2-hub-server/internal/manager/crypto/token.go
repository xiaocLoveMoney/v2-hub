package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// RandomToken 返回 n 字节随机数的 URL 安全 Base64（无填充）编码。
func RandomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// SHA256 返回 s 的 SHA-256 摘要，用于只在库里存 token 的摘要。
func SHA256(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
