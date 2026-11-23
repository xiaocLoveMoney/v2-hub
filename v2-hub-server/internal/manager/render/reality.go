package render

import (
	"encoding/json"
	"hash/fnv"
	"net/url"
	"slices"
	"strings"
)

// 面板为 REALITY 入站生成分享链接时，从入站的 short id 与 serverNames 列表里随机取一项，同一 client 每次取到的链接可能不同；
// 平台取回后按 client 固定选取，让链接稳定（docs/03 §5.4 节点来源，Ruling-92）。

const (
	paramSecurity  = "security"
	paramPublicKey = "pbk"
	paramShortID   = "sid"
	paramSNI       = "sni"

	querySep    = "?"
	fragmentSep = "#"
	pairSep     = "&"
	keyValueSep = "="
)

// RealityChoices 是一个 REALITY 入站可供链接选用的值：公钥用来认出链接属于哪个入站。
type RealityChoices struct {
	PublicKey   string
	ShortIDs    []string
	ServerNames []string
}

// realityConfig 是入站配置（Xray 形状：settings / streamSettings / sniffing 三段）里用到的 REALITY 字段。
type realityConfig struct {
	StreamSettings struct {
		Security        string `json:"security"`
		RealitySettings struct {
			ShortIDs    []string `json:"shortIds"`
			ServerNames []string `json:"serverNames"`
			Settings    struct {
				PublicKey string `json:"publicKey"`
			} `json:"settings"`
		} `json:"realitySettings"`
	} `json:"streamSettings"`
}

// RealityChoicesOf 从入站配置取出 REALITY 可选值；不是 REALITY 入站、配置解不开或没有公钥时返回 false。
func RealityChoicesOf(config json.RawMessage) (RealityChoices, bool) {
	var cfg realityConfig
	if err := json.Unmarshal(config, &cfg); err != nil || cfg.StreamSettings.Security != SecReality {
		return RealityChoices{}, false
	}
	rs := cfg.StreamSettings.RealitySettings
	if rs.Settings.PublicKey == "" {
		return RealityChoices{}, false
	}
	return RealityChoices{PublicKey: rs.Settings.PublicKey, ShortIDs: rs.ShortIDs, ServerNames: rs.ServerNames}, true
}

// PinReality 返回把 REALITY 链接的 sid / sni 换成按 key（client 的稳定标识）在所属入站列表里固定选取的值后的链接。
// 所属入站按公钥认：公钥相同、且链接的 sid 或 sni 在该入站列表里；认不出或认出多个时原样返回。
// 链接里的值不在列表里（如对外地址自己指定的 SNI）时该项不动。只改这两个参数，链接其余部分逐字保留。
func PinReality(link, key string, choices []RealityChoices) string {
	head, fragment, hasFragment := strings.Cut(link, fragmentSep)
	base, rawQuery, hasQuery := strings.Cut(head, querySep)
	if !hasQuery || strings.HasPrefix(strings.ToLower(strings.TrimSpace(base)), schemeVMess+schemeSep) {
		return link
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil || q.Get(paramSecurity) != SecReality {
		return link
	}
	sid, sni := q.Get(paramShortID), q.Get(paramSNI)
	owner, ok := realityOwner(choices, q.Get(paramPublicKey), sid, sni)
	if !ok {
		return link
	}
	repl := make(map[string]string, 2)
	if q.Has(paramShortID) && slices.Contains(owner.ShortIDs, sid) {
		repl[paramShortID] = pickStable(owner.ShortIDs, key)
	}
	if q.Has(paramSNI) && slices.Contains(owner.ServerNames, sni) {
		repl[paramSNI] = pickStable(owner.ServerNames, key)
	}
	pinned := base + querySep + replaceQueryValues(rawQuery, repl)
	if hasFragment {
		pinned += fragmentSep + fragment
	}
	return pinned
}

// realityOwner 找出链接所属的入站：公钥相同且 sid 或 sni 在其列表里；恰好一个（列表相同的算同一个）时返回。
func realityOwner(choices []RealityChoices, publicKey, sid, sni string) (RealityChoices, bool) {
	var owner RealityChoices
	found := false
	for _, c := range choices {
		isMember := slices.Contains(c.ShortIDs, sid) || slices.Contains(c.ServerNames, sni)
		if publicKey == "" || c.PublicKey != publicKey || !isMember {
			continue
		}
		isSame := slices.Equal(c.ShortIDs, owner.ShortIDs) && slices.Equal(c.ServerNames, owner.ServerNames)
		if found && !isSame {
			return RealityChoices{}, false
		}
		owner, found = c, true
	}
	return owner, found
}

// pickStable 按 key 的哈希在 list 里固定取一项（list 非空，由调用方保证）。
func pickStable(list []string, key string) string {
	h := fnv.New32a()
	// fnv 的 Write 不会返回错误
	_, _ = h.Write([]byte(key))
	return list[h.Sum32()%uint32(len(list))]
}

// replaceQueryValues 按原顺序改写查询串里指定参数的值，其余参数逐字保留。
func replaceQueryValues(rawQuery string, repl map[string]string) string {
	if len(repl) == 0 {
		return rawQuery
	}
	pairs := strings.Split(rawQuery, pairSep)
	for i, pair := range pairs {
		rawKey, _, _ := strings.Cut(pair, keyValueSep)
		k, err := url.QueryUnescape(rawKey)
		if err != nil {
			continue
		}
		if v, ok := repl[k]; ok {
			pairs[i] = rawKey + keyValueSep + url.QueryEscape(v)
		}
	}
	return strings.Join(pairs, pairSep)
}
