package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// CanonicalHash 计算 v 的规范 JSON（对象键排序）的 SHA-256 十六进制串，用于漂移判定。
// 先经一次 JSON 往返转成通用结构，保证同一份配置无论字段顺序如何都得到同一摘要。
func CanonicalHash(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal for hash: %w", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return "", fmt.Errorf("normalize for hash: %w", err)
	}
	canonical, err := json.Marshal(generic) // encoding/json 对 map 键排序
	if err != nil {
		return "", fmt.Errorf("marshal canonical: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
