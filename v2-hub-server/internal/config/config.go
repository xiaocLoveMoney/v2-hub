// Package config 负责加载 hub-server 的 YAML 配置。
//
// 配置文件入库（etc/hub-server.yaml），凭据（数据库口令、加密密钥、MinIO 密钥）
// 一律从环境变量读取，不出现在配置文件里（Ruling-2）。
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	defaultListenAddr      = ":8080"
	defaultReadTimeout     = 10 * time.Second
	defaultWriteTimeout    = 30 * time.Second
	defaultShutdownTimeout = 15 * time.Second
	defaultDBMaxConns      = 10
	defaultDBPingTimeout   = 2 * time.Second

	// 会话与登录频控默认值，语义见 docs/03 §4.2
	defaultSessionIdleTimeout   = 2 * time.Hour
	defaultSessionMaxAge        = 7 * 24 * time.Hour
	defaultSessionTouchInterval = time.Minute
	defaultSessionCleanup       = 10 * time.Minute
	defaultLoginMaxFailures     = 5
	defaultLoginWindow          = 15 * time.Minute
	defaultLoginLockout         = 15 * time.Minute
	defaultLoginMaxTrackedKeys  = 10000

	// 面板探测与同步默认值，语义见 docs/03 §5.1
	defaultPanelProbeInterval  = 30 * time.Second
	defaultPanelSyncInterval   = 5 * time.Minute
	defaultPanelRequestTimeout = 15 * time.Second
	defaultPanelConcurrency    = 8
)

// ErrInvalidConfig 表示配置内容不合法。
var ErrInvalidConfig = errors.New("invalid config")

// Config 是 hub-server 的完整配置。
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Auth     AuthConfig     `yaml:"auth"`
	Panel    PanelConfig    `yaml:"panel"`
}

// PanelConfig 是面板探测、同步与请求参数（docs/03 §5.1）。
type PanelConfig struct {
	ProbeInterval  time.Duration `yaml:"probeInterval"`
	SyncInterval   time.Duration `yaml:"syncInterval"`
	RequestTimeout time.Duration `yaml:"requestTimeout"`
	// Concurrency 是同时探测 / 同步的服务器数上限。
	Concurrency int `yaml:"concurrency"`
}

// AuthConfig 是管理员会话与登录频控参数（docs/03 §4.2）。
type AuthConfig struct {
	// SecureCookie 为 true 时会话 Cookie 只经 HTTPS 发送；本机 HTTP 开发由环境变量 HUB_INSECURE_DEV_COOKIE=1 关闭。
	SecureCookie         bool          `yaml:"secureCookie"`
	SessionIdleTimeout   time.Duration `yaml:"sessionIdleTimeout"`
	SessionMaxAge        time.Duration `yaml:"sessionMaxAge"`
	SessionTouchInterval time.Duration `yaml:"sessionTouchInterval"`
	// SessionCleanupInterval 是清理过期会话的周期。
	SessionCleanupInterval time.Duration `yaml:"sessionCleanupInterval"`
	LoginMaxFailures       int           `yaml:"loginMaxFailures"`
	LoginWindow            time.Duration `yaml:"loginWindow"`
	LoginLockout           time.Duration `yaml:"loginLockout"`
	LoginMaxTrackedKeys    int           `yaml:"loginMaxTrackedKeys"`
}

// DatabaseConfig 是 PostgreSQL 连接池参数。连接串（含口令）只从环境变量 HUB_DATABASE_URL 读（Ruling-2）。
type DatabaseConfig struct {
	// MaxConns 连接池最大连接数。
	MaxConns int32 `yaml:"maxConns"`
	// PingTimeout 是 /readyz 探测数据库的超时。
	PingTimeout time.Duration `yaml:"pingTimeout"`
}

// ServerConfig 是 HTTP 服务相关配置。
type ServerConfig struct {
	// ListenAddr 监听地址，形如 ":8080" 或 "127.0.0.1:8080"。
	ListenAddr string `yaml:"listenAddr"`
	// ReadTimeout 读取整个请求（含 body）的超时。
	ReadTimeout time.Duration `yaml:"readTimeout"`
	// WriteTimeout 写响应的超时。
	WriteTimeout time.Duration `yaml:"writeTimeout"`
	// ShutdownTimeout 优雅退出时等待在途请求的最长时间。
	ShutdownTimeout time.Duration `yaml:"shutdownTimeout"`
	// TrustedProxies 是可采信 X-Forwarded-For 的直连网段（CIDR），如 Caddy 所在 docker 网络。
	TrustedProxies []string `yaml:"trustedProxies"`
}

// Load 读取并校验 path 指向的 YAML 配置，未填写的字段使用默认值。
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := defaults()
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %s: %w", path, err)
	}
	return cfg, nil
}

func defaults() Config {
	return Config{Server: ServerConfig{
		ListenAddr:      defaultListenAddr,
		ReadTimeout:     defaultReadTimeout,
		WriteTimeout:    defaultWriteTimeout,
		ShutdownTimeout: defaultShutdownTimeout,
	}, Database: DatabaseConfig{
		MaxConns:    defaultDBMaxConns,
		PingTimeout: defaultDBPingTimeout,
	}, Auth: AuthConfig{
		SecureCookie:           true,
		SessionIdleTimeout:     defaultSessionIdleTimeout,
		SessionMaxAge:          defaultSessionMaxAge,
		SessionTouchInterval:   defaultSessionTouchInterval,
		SessionCleanupInterval: defaultSessionCleanup,
		LoginMaxFailures:       defaultLoginMaxFailures,
		LoginWindow:            defaultLoginWindow,
		LoginLockout:           defaultLoginLockout,
		LoginMaxTrackedKeys:    defaultLoginMaxTrackedKeys,
	}, Panel: PanelConfig{
		ProbeInterval:  defaultPanelProbeInterval,
		SyncInterval:   defaultPanelSyncInterval,
		RequestTimeout: defaultPanelRequestTimeout,
		Concurrency:    defaultPanelConcurrency,
	}}
}

func (c Config) validate() error {
	if c.Server.ListenAddr == "" {
		return fmt.Errorf("%w: server.listenAddr is empty", ErrInvalidConfig)
	}
	if c.Server.ReadTimeout <= 0 || c.Server.WriteTimeout <= 0 || c.Server.ShutdownTimeout <= 0 {
		return fmt.Errorf("%w: server timeouts must be positive", ErrInvalidConfig)
	}
	if c.Database.MaxConns <= 0 || c.Database.PingTimeout <= 0 {
		return fmt.Errorf("%w: database.maxConns and database.pingTimeout must be positive", ErrInvalidConfig)
	}
	a := c.Auth
	if a.SessionIdleTimeout <= 0 || a.SessionMaxAge < a.SessionIdleTimeout ||
		a.SessionTouchInterval <= 0 || a.SessionTouchInterval >= a.SessionIdleTimeout || a.SessionCleanupInterval <= 0 {
		return fmt.Errorf("%w: auth session durations out of range", ErrInvalidConfig)
	}
	if a.LoginMaxFailures <= 0 || a.LoginWindow <= 0 || a.LoginLockout <= 0 || a.LoginMaxTrackedKeys <= 0 {
		return fmt.Errorf("%w: auth login rate limit must be positive", ErrInvalidConfig)
	}
	pc := c.Panel
	if pc.ProbeInterval <= 0 || pc.SyncInterval <= 0 || pc.RequestTimeout <= 0 || pc.Concurrency <= 0 {
		return fmt.Errorf("%w: panel intervals, timeout and concurrency must be positive", ErrInvalidConfig)
	}
	return nil
}
