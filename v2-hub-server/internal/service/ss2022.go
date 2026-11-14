package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"v2-hub-server/internal/dao/sqlcgen"
)

// SS2022 密钥（docs/03 §5.4 SS2022 密钥，Ruling-60）：SS2022 要求面板 client 的 password 是标准 Base64 的 16 或 32 字节密钥，
// 而一台服务器上各协议共用这个字段，所以平台为每个用户另存一把 32 字节密钥，在挂了 SS2022 入站的服务器上使用。

const (
	ss2022MethodPrefix = "2022-blake3-"
	ss2022AES128Method = "2022-blake3-aes-128-gcm"
	ss2022ShortKeyLen  = 16
	ss2022FullKeyLen   = 32
	// ss2022KeyAttempts 是避开 + / 的最多重抽次数；每次满足的概率约四分之一，抽满仍不成功几乎不可能
	ss2022KeyAttempts = 256
	// linkUnsafeBase64 是标准 Base64 里分享链接需要转义的字符
	linkUnsafeBase64 = "+/"

	msgSS2022Mixed = "SS2022 加密方式不一致：这台服务器上该用户挂的 SS2022 入站既有 128 位又有 256 位，请统一"
)

var (
	errSS2022Mixed  = errors.New(msgSS2022Mixed)
	errSS2022NoKey  = errors.New("ss2022 key missing")
	errSS2022BadKey = errors.New("ss2022 key malformed")
)

// newSS2022Key 生成 32 字节随机密钥的标准 Base64；整把与前 16 字节两种编码都不含 + /，分享链接里不用转义。
func newSS2022Key() (string, error) {
	buf := make([]byte, ss2022FullKeyLen)
	for range ss2022KeyAttempts {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("random ss2022 key: %w", err)
		}
		full := base64.StdEncoding.EncodeToString(buf)
		short := base64.StdEncoding.EncodeToString(buf[:ss2022ShortKeyLen])
		if !strings.ContainsAny(full, linkUnsafeBase64) && !strings.ContainsAny(short, linkUnsafeBase64) {
			return full, nil
		}
	}
	return "", errors.New("random ss2022 key: no link-safe key found")
}

// ss2022KeyLen 返回加密方式要求的密钥字节数；不是 SS2022 返回 0。
func ss2022KeyLen(method string) int {
	switch {
	case !strings.HasPrefix(method, ss2022MethodPrefix):
		return 0
	case method == ss2022AES128Method:
		return ss2022ShortKeyLen
	default:
		return ss2022FullKeyLen
	}
}

// ss2022KeyLenOf 返回一组入站要求的 SS2022 密钥字节数（0 = 没有 SS2022）；128 位与 256 位混用时返回 errSS2022Mixed。
// refLen 是这台服务器上各 Shadowsocks 入站的密钥字节数。
func ss2022KeyLenOf(refs []string, refLen map[string]int) (int, error) {
	n := 0
	for _, r := range refs {
		l := refLen[r]
		if l == 0 {
			continue
		}
		if n != 0 && n != l {
			return 0, errSS2022Mixed
		}
		n = l
	}
	return n, nil
}

// ss2022KeyFor 按字节数取密钥：32 用整把，16 用前 16 字节重新编码。
func ss2022KeyFor(key string, n int) (string, error) {
	if key == "" {
		return "", errSS2022NoKey
	}
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != ss2022FullKeyLen {
		return "", errSS2022BadKey
	}
	return base64.StdEncoding.EncodeToString(raw[:n]), nil
}

// EnsureSS2022Keys 给还没有 SS2022 密钥的用户补发（M10 之前的老用户，Ruling-60），凭据版本 +1 交给对账写到面板。
// 启动时调用一次，返回补发的人数。
func (s *UserService) EnsureSS2022Keys(ctx context.Context) (int, error) {
	rows, err := s.q.ListUserCredentials(ctx)
	if err != nil {
		return 0, fmt.Errorf("list user credentials: %w", err)
	}
	filled := 0
	for _, row := range rows {
		cred, err := s.openCredential(row.ID, row.ClientCredEnc)
		if err != nil {
			return filled, err
		}
		if cred.SSKey != "" {
			continue
		}
		if cred.SSKey, err = newSS2022Key(); err != nil {
			return filled, err
		}
		sealed, err := s.sealCredential(row.ID, cred)
		if err != nil {
			return filled, err
		}
		err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
			if _, err := q.UpdateUserCredential(ctx, sqlcgen.UpdateUserCredentialParams{ID: row.ID, ClientCredEnc: sealed}); err != nil {
				return fmt.Errorf("update credential of user %d: %w", row.ID, err)
			}
			if err := q.MarkUserProvisionsPending(ctx, row.ID); err != nil {
				return fmt.Errorf("mark provisions of user %d pending: %w", row.ID, err)
			}
			return nil
		})
		if err != nil {
			return filled, err
		}
		filled++
	}
	if filled > 0 {
		slog.Info("filled ss2022 keys", "users", filled)
		s.TriggerReconcile()
	}
	return filled, nil
}
