// Package totp 实现 RFC 6238 基于时间的一次性密码（HMAC-SHA1、30 秒步长、6 位），兼容常见验证器 App。
// 选型与参数见 docs/03 §4.3。
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 默认算法，验证器 App 兼容性要求；这里用作 HMAC，不作摘要防碰撞
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"time"
)

const (
	// SecretBytes 是密钥长度（160 bit，RFC 4226 推荐值）。
	SecretBytes = 20
	// Period 是步长。
	Period = 30 * time.Second
	// Digits 是验证码位数。
	Digits = 6
	// Skew 是允许的前后步长偏差数。
	Skew          = 1
	digitsMod     = 1_000_000
	codeFormat    = "%06d"
	dynOffsetMask = 0x0f
	dynBinaryMask = 0x7fffffff
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret 生成随机密钥，返回原始字节与供手动输入的 Base32 文本。
func GenerateSecret() ([]byte, string, error) {
	raw := make([]byte, SecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("generate totp secret: %w", err)
	}
	return raw, b32.EncodeToString(raw), nil
}

// Step 返回 t 所在的步长序号。
func Step(t time.Time) int64 {
	return t.Unix() / int64(Period/time.Second)
}

// CodeAt 计算某个步长的验证码。
func CodeAt(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & dynOffsetMask
	bin := binary.BigEndian.Uint32(sum[offset:offset+4]) & dynBinaryMask
	return fmt.Sprintf(codeFormat, bin%digitsMod)
}

// Verify 校验 code：在 now 前后 Skew 个步长内、且步长大于 lastStep（防重放）时通过，返回命中的步长。
func Verify(secret []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	if len(code) != Digits {
		return 0, false
	}
	current := Step(now)
	for step := current - Skew; step <= current+Skew; step++ {
		if step <= lastStep {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(CodeAt(secret, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// URI 生成 otpauth:// 地址，验证器 App 扫码即可添加。
func URI(issuer, account, secretB32 string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secretB32)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(Digits))
	q.Set("period", fmt.Sprint(int(Period/time.Second)))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
