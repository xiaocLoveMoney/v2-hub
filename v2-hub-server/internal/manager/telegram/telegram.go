// Package telegram 封装 Telegram Bot API 的发消息调用（docs/03 §5.20，Ruling-42）。
// token 只来自环境变量；请求地址里含 token，本包返回的错误一律不带地址，调用方可以放心记日志。
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultAPIBase 是官方 Bot API 地址；联调时可换成模拟端。
	DefaultAPIBase   = "https://api.telegram.org"
	defaultTimeout   = 15 * time.Second
	maxResponseBytes = 1 << 20
	maxTextRunes     = 4096 // Bot API 单条消息上限
	maxDescription   = 200
	pathSendMessage  = "/bot%s/sendMessage"
	contentTypeJSON  = "application/json"
	headerContent    = "Content-Type"
)

var (
	// ErrUnreachable 表示连不上 Bot API 或响应无法解析（网络、超时、TLS、5xx）。
	ErrUnreachable = errors.New("telegram api unreachable")
	// ErrRejected 表示 Bot API 拒绝了请求（token 无效、会话不存在、机器人被移出等）。
	ErrRejected = errors.New("telegram api rejected the request")
	// ErrBadConfig 表示配置不合法。
	ErrBadConfig = errors.New("telegram config is invalid")
)

// Config 是机器人配置。Token 为空即 Telegram 不可用。
type Config struct {
	Token string
	// APIBase 为空时取官方地址。
	APIBase string
	Timeout time.Duration
}

// Sender 发一条纯文本消息到会话。
type Sender interface {
	SendMessage(ctx context.Context, chatID, text string) error
}

// Client 是 Bot API 客户端，实现 Sender。并发安全。
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New 按配置创建客户端；Token 为空返回 (nil, nil)，表示不可用。
func New(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, nil
	}
	base := cfg.APIBase
	if base == "" {
		base = DefaultAPIBase
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%w: api base must be an http(s) url", ErrBadConfig)
	}
	if strings.ContainsAny(cfg.Token, "/?#") {
		return nil, fmt.Errorf("%w: token contains url delimiters", ErrBadConfig)
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	return &Client{base: strings.TrimRight(base, "/"), token: cfg.Token, http: &http.Client{Timeout: timeout}}, nil
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// SendMessage 实现 Sender：纯文本（不启用 Markdown / HTML 解析），超长截断到 4096 字。
func (c *Client) SendMessage(ctx context.Context, chatID, text string) (retErr error) {
	if r := []rune(text); len(r) > maxTextRunes {
		text = string(r[:maxTextRunes])
	}
	body, err := json.Marshal(map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true})
	if err != nil {
		return fmt.Errorf("telegram: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+fmt.Sprintf(pathSendMessage, c.token), bytes.NewReader(body))
	if err != nil {
		// 构造失败的错误里可能带地址，不包装
		return fmt.Errorf("%w: build request", ErrBadConfig)
	}
	req.Header.Set(headerContent, contentTypeJSON)
	resp, err := c.http.Do(req)
	if err != nil {
		// *url.Error 带完整地址（含 token），只保留其内层原因
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("%w: %v", ErrUnreachable, ue.Err)
		}
		return ErrUnreachable
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("telegram: close body: %w", cerr)
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: read response", ErrUnreachable)
	}
	var out apiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("%w: HTTP %d", ErrUnreachable, resp.StatusCode)
	}
	if !out.OK {
		desc := []rune(out.Description)
		if len(desc) > maxDescription {
			desc = desc[:maxDescription]
		}
		if resp.StatusCode >= http.StatusInternalServerError {
			return fmt.Errorf("%w: %s", ErrUnreachable, string(desc))
		}
		return fmt.Errorf("%w: %s", ErrRejected, string(desc))
	}
	return nil
}
