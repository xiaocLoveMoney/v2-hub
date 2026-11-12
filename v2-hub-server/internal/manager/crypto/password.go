// Package crypto 封装口令哈希、随机 token 与摘要等通用密码学能力（manager 层）。
package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id 参数取 OWASP 基线（docs/03 §4.2）：m=19 MiB, t=2, p=1。
const (
	argonMemoryKiB  uint32 = 19 * 1024
	argonIterations uint32 = 2
	argonThreads    uint8  = 1
	argonKeyLen     uint32 = 32
	argonSaltLen           = 16
	argonVersion           = argon2.Version
	phcPrefix              = "$argon2id$"
	phcFieldCount          = 6
)

// ErrMalformedHash 表示存储的口令哈希格式无法解析。
var ErrMalformedHash = errors.New("malformed password hash")

var b64 = base64.RawStdEncoding

// HashPassword 用 argon2id 计算口令哈希，返回 PHC 格式字符串（含盐与参数）。
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("%sv=%d$m=%d,t=%d,p=%d$%s$%s",
		phcPrefix, argonVersion, argonMemoryKiB, argonIterations, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword 校验口令是否与 PHC 格式哈希匹配；按哈希里记录的参数重算，参数升级后旧哈希仍可校验。
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != phcFieldCount || !strings.HasPrefix(encoded, phcPrefix) {
		return false, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argonVersion {
		return false, ErrMalformedHash
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, ErrMalformedHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, ErrMalformedHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, ErrMalformedHash
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
