package xui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"v2-hub-server/internal/manager/panel"
)

// 3x-ui 入站 JSON 的键名。
const (
	keySettings       = "settings"
	keyStreamSettings = "streamSettings"
	keySniffing       = "sniffing"
	keyClients        = "clients"
	keyPeers          = "peers"
	keyEnable         = "enable"
	keyRemark         = "remark"
	keyPort           = "port"
	keyListen         = "listen"
	keyProtocol       = "protocol"
	keyExpiryTime     = "expiryTime"
	keyTotal          = "total"
)

// inboundWire 是 3x-ui 返回的入站中平台用到的字段；其余字段在 raw 中原样保留。
type inboundWire struct {
	ID       int64  `json:"id"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Listen   string `json:"listen"`
	Tag      string `json:"tag"`
	Remark   string `json:"remark"`
	Enable   bool   `json:"enable"`
}

// configWire 是入站的内核配置三段（Xray 形状）。
type configWire struct {
	Settings       map[string]any `json:"settings"`
	StreamSettings map[string]any `json:"streamSettings"`
	Sniffing       map[string]any `json:"sniffing"`
}

// inboundRecord 一次解出入站的平台字段与三段配置，避免对同一份 JSON 解码两遍。
type inboundRecord struct {
	inboundWire
	configWire
}

// sensitivePaths 是缓存副本里要删除、编辑时要从面板补回的字段（Ruling-14）。
// 每项是从 configWire 根开始的路径；"*" 表示数组的每个元素。
var sensitivePaths = [][]string{
	{keyStreamSettings, "realitySettings", "privateKey"},
	{keyStreamSettings, "realitySettings", "mldsa65Seed"},
	{keyStreamSettings, "tlsSettings", "certificates", "*", "key"},
	{keyStreamSettings, "tlsSettings", "echServerKeys"},
	{keySettings, "password"},
	{keySettings, "secretKey"},
	{keySettings, "decryption"},
}

// keyGenPaths 是各类密钥在面板上的生成接口。
var keyGenPaths = map[panel.KeyKind]string{
	panel.KeyX25519:   "/server/getNewX25519Cert",
	panel.KeyMLDSA65:  "/server/getNewmldsa65",
	panel.KeyVLESSEnc: "/server/getNewVlessEnc",
}

// toNeutral 把一条 3x-ui 入站 JSON 转成中立模型：计算摘要、删除敏感字段与 clients。
func toNeutral(raw json.RawMessage) (panel.Inbound, error) {
	var rec inboundRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return panel.Inbound{}, fmt.Errorf("decode inbound: %w", err)
	}
	w, cfg := rec.inboundWire, rec.configWire
	clientCount := countClients(cfg.Settings)

	// 摘要覆盖完整配置（含敏感字段，私钥被换也算面板侧改动），但不含 clients 与流量计数
	hashSettings := withoutKeys(cfg.Settings, keyClients, keyPeers)
	hash, err := panel.CanonicalHash(map[string]any{
		keyProtocol: w.Protocol, keyPort: w.Port, keyListen: w.Listen, keyRemark: w.Remark, keyEnable: w.Enable,
		keySettings: hashSettings, keyStreamSettings: cfg.StreamSettings, keySniffing: cfg.Sniffing,
	})
	if err != nil {
		return panel.Inbound{}, err
	}

	// cfg 是本函数刚解出的私有数据，摘要已算完，脱敏可直接在其上进行，无需再深拷贝；
	// 只有缺省段落要补成空对象，与原先 deepCopy(nil) 的结果一致。
	network, security := stringAt(cfg.StreamSettings, "network"), stringAt(cfg.StreamSettings, "security")
	redacted := configWire{
		Settings:       hashSettings,
		StreamSettings: orEmpty(cfg.StreamSettings),
		Sniffing:       orEmpty(cfg.Sniffing),
	}
	root := map[string]any{keySettings: redacted.Settings, keyStreamSettings: redacted.StreamSettings}
	for _, path := range sensitivePaths {
		deletePath(root, path)
	}
	configJSON, err := json.Marshal(redacted)
	if err != nil {
		return panel.Inbound{}, fmt.Errorf("encode redacted config: %w", err)
	}

	return panel.Inbound{
		Ref:         strconv.FormatInt(w.ID, 10),
		Protocol:    w.Protocol,
		Port:        w.Port,
		Listen:      w.Listen,
		Tag:         w.Tag,
		Remark:      w.Remark,
		Enabled:     w.Enable,
		Network:     network,
		Security:    security,
		CoreType:    panel.CoreXray,
		Config:      configJSON,
		ConfigHash:  hash,
		ClientCount: clientCount,
	}, nil
}

func countClients(settings map[string]any) int {
	for _, k := range []string{keyClients, keyPeers} {
		if list, ok := settings[k].([]any); ok {
			return len(list)
		}
	}
	return 0
}

// ListInbounds 实现 panel.Provider。
func (c *Client) ListInbounds(ctx context.Context) ([]panel.Inbound, error) {
	var raws []json.RawMessage
	if err := c.call(ctx, http.MethodGet, "/inbounds/list", nil, &raws); err != nil {
		return nil, err
	}
	out := make([]panel.Inbound, 0, len(raws))
	for _, r := range raws {
		in, err := toNeutral(r)
		if err != nil {
			return nil, &panel.Error{Kind: panel.KindUnreachable, Message: "面板返回的入站格式与预期不符", Err: err}
		}
		out = append(out, in)
	}
	return out, nil
}

// checkRef 校验入站标识是 3x-ui 的数字 id；不合法按入站不存在处理。
func checkRef(ref string) error {
	if _, err := strconv.ParseInt(ref, 10, 64); err != nil {
		return &panel.Error{Kind: panel.KindNotFound, Message: "入站标识不合法"}
	}
	return nil
}

func (c *Client) getRaw(ctx context.Context, ref string) (map[string]any, error) {
	if err := checkRef(ref); err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := c.call(ctx, http.MethodGet, "/inbounds/get/"+ref, nil, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, &panel.Error{Kind: panel.KindNotFound, Message: "入站不存在"}
	}
	return raw, nil
}

// GetInbound 实现 panel.Provider。
func (c *Client) GetInbound(ctx context.Context, ref string) (panel.Inbound, error) {
	raw, err := c.getRaw(ctx, ref)
	if err != nil {
		return panel.Inbound{}, err
	}
	return neutralFromMap(raw)
}

// GetInboundConfig 实现 panel.Provider：原样取面板上的三段配置，只去掉 clients / peers（归 M2 下发对账）。
func (c *Client) GetInboundConfig(ctx context.Context, ref string) (panel.InboundConfig, error) {
	raw, err := c.getRaw(ctx, ref)
	if err != nil {
		return panel.InboundConfig{}, err
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return panel.InboundConfig{}, fmt.Errorf("encode inbound: %w", err)
	}
	var rec inboundRecord
	if err := json.Unmarshal(encoded, &rec); err != nil {
		return panel.InboundConfig{}, &panel.Error{Kind: panel.KindUnreachable, Message: "面板返回的入站格式与预期不符", Err: err}
	}
	w, cfg := rec.inboundWire, rec.configWire
	cfg.Settings = withoutKeys(cfg.Settings, keyClients, keyPeers)
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		return panel.InboundConfig{}, fmt.Errorf("encode inbound config: %w", err)
	}
	return panel.InboundConfig{
		Protocol: w.Protocol, Port: w.Port, Listen: w.Listen, Remark: w.Remark, Enabled: w.Enable, Config: configJSON,
	}, nil
}

// GenerateKeys 实现 panel.Provider，调用面板自带的密钥生成接口。
func (c *Client) GenerateKeys(ctx context.Context, kind panel.KeyKind) (json.RawMessage, error) {
	path, ok := keyGenPaths[kind]
	if !ok {
		return nil, &panel.Error{Kind: panel.KindRejected, Message: "不支持的密钥种类"}
	}
	var out json.RawMessage
	if err := c.call(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func neutralFromMap(raw map[string]any) (panel.Inbound, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return panel.Inbound{}, fmt.Errorf("encode inbound: %w", err)
	}
	in, err := toNeutral(encoded)
	if err != nil {
		return panel.Inbound{}, &panel.Error{Kind: panel.KindUnreachable, Message: "面板返回的入站格式与预期不符", Err: err}
	}
	return in, nil
}

// decodeSpecConfig 解出 spec.Config 中的三段配置；缺省段落给空对象。
func decodeSpecConfig(spec panel.InboundSpec) (configWire, error) {
	var cfg configWire
	if len(spec.Config) > 0 {
		if err := json.Unmarshal(spec.Config, &cfg); err != nil {
			return configWire{}, &panel.Error{Kind: panel.KindRejected, Message: "入站配置不是合法的 JSON 对象", Err: err}
		}
	}
	if cfg.Settings == nil {
		cfg.Settings = map[string]any{}
	}
	if cfg.StreamSettings == nil {
		cfg.StreamSettings = map[string]any{}
	}
	if cfg.Sniffing == nil {
		cfg.Sniffing = map[string]any{}
	}
	return cfg, nil
}

// CreateInbound 实现 panel.Provider。新建入站不带 clients（M2 由下发对账挂载）。
func (c *Client) CreateInbound(ctx context.Context, spec panel.InboundSpec) (panel.Inbound, error) {
	cfg, err := decodeSpecConfig(spec)
	if err != nil {
		return panel.Inbound{}, err
	}
	if _, ok := cfg.Settings[keyClients]; !ok {
		cfg.Settings[keyClients] = []any{}
	}
	body := map[string]any{
		keyEnable: spec.Enabled, keyRemark: spec.Remark, keyListen: spec.Listen, keyPort: spec.Port,
		keyProtocol: spec.Protocol, keyExpiryTime: 0, keyTotal: 0,
		keySettings: cfg.Settings, keyStreamSettings: cfg.StreamSettings, keySniffing: cfg.Sniffing,
	}
	var created map[string]any
	if err := c.call(ctx, http.MethodPost, "/inbounds/add", body, &created); err != nil {
		return panel.Inbound{}, err
	}
	return neutralFromMap(created)
}

// UpdateInbound 实现 panel.Provider：以面板现有入站为底，覆盖平台管理的字段；
// spec.Config 为空表示只改基本字段；省略的敏感字段与现有 clients 一律从面板原值补回（Ruling-14）。
func (c *Client) UpdateInbound(ctx context.Context, ref string, spec panel.InboundSpec) (panel.Inbound, error) {
	current, err := c.getRaw(ctx, ref)
	if err != nil {
		return panel.Inbound{}, err
	}
	merged := deepCopy(current)
	merged[keyRemark] = spec.Remark
	merged[keyPort] = spec.Port
	merged[keyListen] = spec.Listen
	merged[keyEnable] = spec.Enabled
	if len(spec.Config) > 0 {
		cfg, err := decodeSpecConfig(spec)
		if err != nil {
			return panel.Inbound{}, err
		}
		curSettings, _ := current[keySettings].(map[string]any)
		for _, k := range []string{keyClients, keyPeers} {
			if v, ok := curSettings[k]; ok {
				cfg.Settings[k] = v
			} else {
				delete(cfg.Settings, k)
			}
		}
		next := map[string]any{keySettings: cfg.Settings, keyStreamSettings: cfg.StreamSettings}
		for _, path := range sensitivePaths {
			restorePath(next, current, path)
		}
		merged[keySettings] = cfg.Settings
		merged[keyStreamSettings] = cfg.StreamSettings
		merged[keySniffing] = cfg.Sniffing
	}
	var updated map[string]any
	if err := c.call(ctx, http.MethodPost, "/inbounds/update/"+ref, merged, &updated); err != nil {
		return panel.Inbound{}, err
	}
	if updated == nil {
		return c.GetInbound(ctx, ref)
	}
	return neutralFromMap(updated)
}

// SetInboundEnabled 实现 panel.Provider。
func (c *Client) SetInboundEnabled(ctx context.Context, ref string, enabled bool) error {
	if err := checkRef(ref); err != nil {
		return err
	}
	return c.call(ctx, http.MethodPost, "/inbounds/setEnable/"+ref, map[string]bool{keyEnable: enabled}, nil)
}

// DeleteInbound 实现 panel.Provider。3x-ui 对不存在的入站也返回成功。
func (c *Client) DeleteInbound(ctx context.Context, ref string) error {
	if err := checkRef(ref); err != nil {
		return err
	}
	return c.call(ctx, http.MethodPost, "/inbounds/del/"+ref, nil, nil)
}
