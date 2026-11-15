package chain

import (
	"encoding/json"
	"fmt"
)

// BlockTag 是平台拦截规则的出站 tag 与路由 ruleTag（docs/03 §5.19 拦截规则，Ruling-55）。
const BlockTag = TagPrefix + "block"

const (
	keyProtocol       = "protocol"
	keySettings       = "settings"
	keyDomain         = "domain"
	protocolBlackhole = "blackhole"
)

// blockDomains 是拦截规则的域名条件，内容固定（Ruling-55）。regexp: 条目由 Xray 按 Go 正则编译，单测保证都能编译；
// ext:geosite_IR.dat / ext:geosite_RU.dat 用 3x-ui 自带的文件。
var blockDomains = []string{
	"geosite:category-ads-all",
	"ext:geosite_IR.dat:category-ads-all",
	"ext:geosite_RU.dat:category-ads-all",
	`regexp:(.+\.|^)(360|so|qihoo|360safe|qhimg|360totalsecurity|yunpan)\.(cn|com)`,
	`regexp:(api|ps|sv|offnavi|newvector|ulog\.imap|newloc)(\.map|)\.(baidu|n\.shifen)\.com`,
	`regexp:(^.*\@)(guerrillamail|guerrillamailblock|sharklasers|grr|pokemail|spam4|bccto|chacuo|027168)\.(info|biz|com|de|net|org|me|la)`,
	`regexp:(.?)(xunlei|sandai|Thunder|XLLiveUD)(.)`,
	`regexp:\b([\w-]+\.)*((esu|zhina|nmsl)\.(wiki|dog|website|lol)|esuwiki\.org|t91y\.com)`,
	`regexp:(.*\.||)(bot|hncb|scsb|taipeifubon|fubon|cathaybk|bok|tbb|tcbbank|hsbc|ktb|bop|ubot|feib|sinopac|dbs|ka-cn|wowoka|mycard520|btgamehk|i7391|omg|288game|gameplayplay|duogamecard|kaletou|158card|gameneed|520game|dacardba|gashpoint|gash|ecard|jhpoint|jiaoyimao|gamebsina|5658ka|ate91|8591|jp87|htmgame|ibon|8591|5173|yooek|4931|7881|cucu91|7nfun|laoyouka|kavip|uaka|youdianka|milandor|jkos|gamapay|opay|allpay|pay2go|taiwanpay|piapp|pchomepay|funcashier)\.(com|net|tw)`,
	`regexp:(.*\.||)(metatrader4|metatrader5|mql5)\.(org|com|net)`,
}

// WithBlock 返回在平台条目最前面加上拦截条目（一个黑洞出站 + 一条按域名拦截的路由）后的新条目，原条目不变。
// 拦截路由排在链式路由之前，服务端链式的流量同样先过拦截。
func (s Section) WithBlock() (Section, error) {
	outbound, err := json.Marshal(map[string]any{keyTag: BlockTag, keyProtocol: protocolBlackhole, keySettings: map[string]any{}})
	if err != nil {
		return Section{}, fmt.Errorf("marshal block outbound: %w", err)
	}
	rule, err := json.Marshal(map[string]any{
		"type": ruleTypeField, keyRuleTag: BlockTag, keyDomain: blockDomains, keyOutboundTag: BlockTag,
	})
	if err != nil {
		return Section{}, fmt.Errorf("marshal block rule: %w", err)
	}
	out := Section{
		Outbounds: make([]json.RawMessage, 0, len(s.Outbounds)+1),
		Rules:     make([]json.RawMessage, 0, len(s.Rules)+1),
	}
	out.Outbounds = append(append(out.Outbounds, outbound), s.Outbounds...)
	out.Rules = append(append(out.Rules, rule), s.Rules...)
	return out, nil
}
