package xui

import (
	"context"
	"encoding/json"
	"net/http"

	"v2-hub-server/internal/manager/panel"
)

// 3x-ui 面板设置相关的路径与字段（contract/upstream/3x-ui/openapi-v3.11.0.json 的 Setting 组）。
const (
	pathSettingAll    = "/setting/all"
	pathSettingUpdate = "/setting/update"
	// keyRestartOnClientDisable 是“client 停用后重启 Xray”的设置项：默认开，任何 client 被停用或删除都整机重启内核。
	keyRestartOnClientDisable = "restartXrayOnClientDisable"
	jsonFalse                 = "false"
)

var _ panel.CoreKeeper = (*Client)(nil)

// KeepCoreOnClientDrop 实现 panel.CoreKeeper（Ruling-94）。面板的设置接口只能整份覆盖：读出全部设置、只改这一项、原样写回；
// 各项的值按原始 JSON 保留，面板对脱敏成空串的密钥按“未改动”处理（v3.11.0 实测）。面板没有这一项（旧版本）时不写。
func (c *Client) KeepCoreOnClientDrop(ctx context.Context) (bool, error) {
	var settings map[string]json.RawMessage
	if err := c.call(ctx, http.MethodPost, pathSettingAll, nil, &settings); err != nil {
		return false, err
	}
	raw, ok := settings[keyRestartOnClientDisable]
	if !ok {
		return false, nil
	}
	var isOn bool
	if err := json.Unmarshal(raw, &isOn); err != nil {
		return false, &panel.Error{Kind: panel.KindUnreachable, Message: "面板返回的设置不是预期的 JSON", Err: err}
	}
	if !isOn {
		return false, nil
	}
	settings[keyRestartOnClientDisable] = json.RawMessage(jsonFalse)
	if err := c.call(ctx, http.MethodPost, pathSettingUpdate, settings, nil); err != nil {
		return false, err
	}
	return true, nil
}
