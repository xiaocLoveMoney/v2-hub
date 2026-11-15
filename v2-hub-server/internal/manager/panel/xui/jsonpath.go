package xui

import "encoding/json"

// 操作 JSON 通用结构（map[string]any / []any）的小工具，供敏感字段删除与补回使用。

const wildcard = "*"

// deletePath 删除 root 中 path 指向的字段；中途缺失时什么也不做。
func deletePath(node any, path []string) {
	if len(path) == 0 {
		return
	}
	switch v := node.(type) {
	case map[string]any:
		if len(path) == 1 {
			delete(v, path[0])
			return
		}
		deletePath(v[path[0]], path[1:])
	case []any:
		if path[0] != wildcard {
			return
		}
		for _, item := range v {
			deletePath(item, path[1:])
		}
	}
}

// restorePath 在 dst 缺少 path 指向的字段时，从 src 的同一位置补回。
// 数组按下标对应（证书列表等通常不会重排）；src 缺失时不补。
func restorePath(dst, src any, path []string) {
	if len(path) == 0 {
		return
	}
	switch d := dst.(type) {
	case map[string]any:
		s, ok := src.(map[string]any)
		if !ok {
			return
		}
		key := path[0]
		if len(path) == 1 {
			if _, present := d[key]; !present {
				if sv, ok := s[key]; ok {
					d[key] = sv
				}
			}
			return
		}
		restorePath(d[key], s[key], path[1:])
	case []any:
		s, ok := src.([]any)
		if !ok || path[0] != wildcard {
			return
		}
		for i := range d {
			if i < len(s) {
				restorePath(d[i], s[i], path[1:])
			}
		}
	}
}

// withoutKeys 返回去掉若干键的浅拷贝。
func withoutKeys(m map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	for _, k := range keys {
		delete(out, k)
	}
	return out
}

// deepCopy 经 JSON 往返深拷贝通用结构，避免修改调用方持有的数据。
func deepCopy(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// orEmpty 把 nil 换成空对象，其余原样返回。
func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func stringAt(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
