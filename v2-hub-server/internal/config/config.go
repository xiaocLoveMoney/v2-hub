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
)

// ErrInvalidConfig 表示配置内容不合法。
var ErrInvalidConfig = errors.New("invalid config")

// Config 是 hub-server 的完整配置。
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
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
	return nil
}
