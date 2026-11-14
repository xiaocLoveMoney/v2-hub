// Package chain 生成服务端链式在入口面板内核模板里的平台条目，并把它们合并进模板（docs/03 §5.19，Ruling-39、Ruling-40）。
// 本包只做纯计算，不调用面板：读写模板、重启与回滚由 service 层经 panel.CoreConfigurator 完成。
// 平台条目含落地凭据，本包不写日志。
package chain

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"v2-hub-server/internal/manager/render"
)

// TagPrefix 是平台条目的 tag 前缀：模板里带这个前缀的出站与路由规则都归平台所有（docs/03 §6）。
const TagPrefix = "hub-"

const (
	keyOutbounds   = "outbounds"
	keyRouting     = "routing"
	keyRules       = "rules"
	keyTag         = "tag"
	keyRuleTag     = "ruleTag"
	keyOutboundTag = "outboundTag"
	keyInboundTag  = "inboundTag"
	apiInboundTag  = "api"
	ruleTypeField  = "field"
)

// Entry 是一条规则在一台起飞服务器上要下发的内容：这条规则在该服务器上生效的入站 tag、用户在该面板上的 client email、落地参数。
type Entry struct {
	RuleID      int64
	InboundTags []string
	UserEmail   string
	Exit        render.ProxyNode
}

// userEmailPattern 是平台生成的 client email 的格式（docs/03 §6：u{用户 id}-{随机码}）。
// Xray 路由的 user 条件把 "regexp:" 开头的值当正则，别的值一旦混进来就可能匹配到其他用户（docs/03 §5.19 平台条目）。
var userEmailPattern = regexp.MustCompile(`^u[0-9]+-[0-9a-z]+$`)

// ErrBadEntry 表示条目不满足下发的前提（email 格式不对、没有入站）；调用方的数据有问题，不应下发。
var ErrBadEntry = errors.New("bad chain entry")

// Tag 返回规则对应的出站 tag 与路由 ruleTag。
func Tag(ruleID int64) string {
	return fmt.Sprintf("%sr%d", TagPrefix, ruleID)
}

// Section 是模板里的平台条目：出站与路由规则，各自按规则 id 升序。
type Section struct {
	Outbounds []json.RawMessage
	Rules     []json.RawMessage
}

// Build 由规则生成平台条目，返回实际纳入的规则 id（升序）。落地的协议或传输 Xray 表达不了的规则跳过，不纳入；
// email 不是平台生成的格式或没有入站时返回 ErrBadEntry。
func Build(entries []Entry) (Section, []int64, error) {
	sorted := slices.Clone(entries)
	slices.SortFunc(sorted, func(a, b Entry) int { return cmp.Compare(a.RuleID, b.RuleID) })
	sec := Section{Outbounds: make([]json.RawMessage, 0, len(sorted)), Rules: make([]json.RawMessage, 0, len(sorted))}
	included := make([]int64, 0, len(sorted))
	for _, e := range sorted {
		if !userEmailPattern.MatchString(e.UserEmail) || len(e.InboundTags) == 0 || slices.Contains(e.InboundTags, "") {
			return Section{}, nil, fmt.Errorf("%w: rule %d", ErrBadEntry, e.RuleID)
		}
		tag := Tag(e.RuleID)
		outbound, ok := render.XrayOutbound(e.Exit, tag)
		if !ok {
			continue
		}
		rawOut, err := json.Marshal(outbound)
		if err != nil {
			return Section{}, nil, fmt.Errorf("marshal outbound of rule %d: %w", e.RuleID, err)
		}
		rule := map[string]any{
			"type": ruleTypeField, keyRuleTag: tag, keyInboundTag: slices.Sorted(slices.Values(e.InboundTags)),
			"user": []string{e.UserEmail}, keyOutboundTag: tag,
		}
		rawRule, err := json.Marshal(rule)
		if err != nil {
			return Section{}, nil, fmt.Errorf("marshal routing rule of rule %d: %w", e.RuleID, err)
		}
		sec.Outbounds = append(sec.Outbounds, rawOut)
		sec.Rules = append(sec.Rules, rawRule)
		included = append(included, e.RuleID)
	}
	return sec, included, nil
}

// Hash 返回平台条目的规范化 SHA-256（键排序、去空白），用于比较面板现状与期望。
func (s Section) Hash() (string, error) {
	canon := func(items []json.RawMessage) ([]any, error) {
		out := make([]any, 0, len(items))
		for _, raw := range items {
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	outbounds, err := canon(s.Outbounds)
	if err != nil {
		return "", fmt.Errorf("canonicalize outbounds: %w", err)
	}
	rules, err := canon(s.Rules)
	if err != nil {
		return "", fmt.Errorf("canonicalize rules: %w", err)
	}
	raw, err := json.Marshal(map[string]any{keyOutbounds: outbounds, keyRules: rules})
	if err != nil {
		return "", fmt.Errorf("marshal section: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Extract 取出模板里现有的平台条目（按模板中的顺序）。
func Extract(config json.RawMessage) (Section, error) {
	_, outbounds, _, rules, err := decode(config)
	if err != nil {
		return Section{}, err
	}
	var sec Section
	for _, o := range outbounds {
		if isHubOutbound(o) {
			sec.Outbounds = append(sec.Outbounds, o)
		}
	}
	for _, r := range rules {
		if isHubRule(r) {
			sec.Rules = append(sec.Rules, r)
		}
	}
	return sec, nil
}

// Merge 返回把模板里的平台条目整体换成 sec 后的新模板：平台出站追加在末尾（不改变默认出站），
// 平台路由规则插在 api 规则之后、其余规则之前；模板的其它部分保持不变。
func Merge(config json.RawMessage, sec Section) (json.RawMessage, error) {
	doc, outbounds, routing, rules, err := decode(config)
	if err != nil {
		return nil, err
	}
	keptOut := make([]json.RawMessage, 0, len(outbounds)+len(sec.Outbounds))
	for _, o := range outbounds {
		if !isHubOutbound(o) {
			keptOut = append(keptOut, o)
		}
	}
	keptOut = append(keptOut, sec.Outbounds...)

	keptRules := make([]json.RawMessage, 0, len(rules)+len(sec.Rules))
	insertAt := 0
	for _, r := range rules {
		if isHubRule(r) {
			continue
		}
		keptRules = append(keptRules, r)
		if isAPIRule(r) {
			insertAt = len(keptRules)
		}
	}
	merged := make([]json.RawMessage, 0, len(keptRules)+len(sec.Rules))
	merged = append(merged, keptRules[:insertAt]...)
	merged = append(merged, sec.Rules...)
	merged = append(merged, keptRules[insertAt:]...)

	if doc[keyOutbounds], err = json.Marshal(keptOut); err != nil {
		return nil, fmt.Errorf("marshal outbounds: %w", err)
	}
	if routing[keyRules], err = json.Marshal(merged); err != nil {
		return nil, fmt.Errorf("marshal routing rules: %w", err)
	}
	if doc[keyRouting], err = json.Marshal(routing); err != nil {
		return nil, fmt.Errorf("marshal routing: %w", err)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal template: %w", err)
	}
	return out, nil
}

// decode 拆出模板的顶层、出站列表、routing 对象与路由规则列表；缺的部分按空处理。
func decode(config json.RawMessage) (map[string]json.RawMessage, []json.RawMessage, map[string]json.RawMessage, []json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(config, &doc); err != nil || doc == nil {
		return nil, nil, nil, nil, fmt.Errorf("core template is not a JSON object: %w", err)
	}
	var outbounds []json.RawMessage
	if raw, ok := doc[keyOutbounds]; ok {
		if err := json.Unmarshal(raw, &outbounds); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("core template outbounds: %w", err)
		}
	}
	routing := map[string]json.RawMessage{}
	if raw, ok := doc[keyRouting]; ok {
		if err := json.Unmarshal(raw, &routing); err != nil || routing == nil {
			return nil, nil, nil, nil, fmt.Errorf("core template routing: %w", err)
		}
	}
	var rules []json.RawMessage
	if raw, ok := routing[keyRules]; ok {
		if err := json.Unmarshal(raw, &rules); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("core template routing rules: %w", err)
		}
	}
	return doc, outbounds, routing, rules, nil
}

// stringField 取对象里某个字符串字段；不是对象或没有该字段返回空串。
func stringField(raw json.RawMessage, key string) string {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	var v string
	if json.Unmarshal(obj[key], &v) != nil {
		return ""
	}
	return v
}

func isHubOutbound(raw json.RawMessage) bool {
	return strings.HasPrefix(stringField(raw, keyTag), TagPrefix)
}

func isHubRule(raw json.RawMessage) bool {
	return strings.HasPrefix(stringField(raw, keyRuleTag), TagPrefix) || strings.HasPrefix(stringField(raw, keyOutboundTag), TagPrefix)
}

// isAPIRule 判断是否面板自己的 api 路由规则（inboundTag 含 api）。
func isAPIRule(raw json.RawMessage) bool {
	var obj struct {
		InboundTag []string `json:"inboundTag"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	return slices.Contains(obj.InboundTag, apiInboundTag)
}
