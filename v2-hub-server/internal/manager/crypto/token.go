package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
)

// RandomToken 返回 n 字节随机数的 URL 安全 Base64（无填充）编码。
func RandomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// RandomString 用 crypto/rand 从 alphabet 里均匀地取 n 个字符拼成串（rand.Int 按字母表长度取值，无取模偏差）。
// alphabet 须为非空的单字节字符集；返回的 error 来自 crypto/rand，由调用方加上下文。
func RandomString(alphabet string, n int) (string, error) {
	var sb strings.Builder
	sb.Grow(n)
	size := big.NewInt(int64(len(alphabet)))
	for range n {
		i, err := rand.Int(rand.Reader, size)
		if err != nil {
			return "", err
		}
		sb.WriteByte(alphabet[i.Int64()])
	}
	return sb.String(), nil
}

// SHA256 返回 s 的 SHA-256 摘要，用于只在库里存 token 的摘要。
func SHA256(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
