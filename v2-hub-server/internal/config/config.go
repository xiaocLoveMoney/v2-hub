// Package config 负责加载 hub-server 的 YAML 配置。
//
// 配置文件入库（etc/hub-server.yaml），凭据（数据库密码、加密密钥、MinIO 密钥）
// 一律从环境变量读取，不出现在配置文件里（Ruling-2）。
package config

import (
	"errors"
	"fmt"
	"net/url"
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

	// 终端用户会话默认值，语义见 docs/03 §5.3
	defaultPortalSessionIdleTimeout = 7 * 24 * time.Hour
	defaultPortalSessionMaxAge      = 30 * 24 * time.Hour
	defaultPortalLoginLogRetention  = 90 * 24 * time.Hour

	// 订阅默认值，语义见 docs/03 §5.4
	defaultSubscriptionTitle         = "v2-hub"
	defaultSubscriptionIntervalHours = 12
	defaultSubscribeLogRetention     = 30 * 24 * time.Hour

	defaultUserTrafficInterval   = time.Minute
	defaultUserReconcileInterval = 5 * time.Minute
	defaultUserTimeZone          = "Local"

	// 服务端链式下发，语义见 docs/03 §5.19
	defaultChainApplyInterval    = 30 * time.Second
	defaultChainMinApplyInterval = 60 * time.Second
	defaultChainUserWriteLimit   = 10
	defaultChainUserWriteWindow  = time.Hour
	defaultChainUserTestLimit    = 6
	defaultChainUserTestWindow   = time.Minute

	// 服务器位置扫描用的在线 IP 库，语义见 docs/03 §5.1（Ruling-65）
	defaultGeoIPLookupURL = "https://api.country.is/{ip}"
	defaultGeoIPTimeout   = 5 * time.Second

	// 总览分析的结果缓存，语义见 docs/03 §5.20 总览分析（Ruling-66）
	defaultAnalyticsCacheTTL = time.Minute

	// 大陆连通检测，语义见 docs/03 §5.20 大陆连通检测（Ruling-67）
	defaultReachAPIURL        = "https://api.globalping.io"
	defaultReachTimeout       = 40 * time.Second
	defaultReachCheckInterval = time.Minute

	// SSH 托管与面板部署，语义见 docs/03 §5.22（Ruling-68、Ruling-71）
	defaultDeployConcurrency    = 3
	defaultDeployPollInterval   = 5 * time.Second
	defaultDeployConnectTimeout = 15 * time.Second
	defaultDeployCommandTimeout = 3 * time.Minute
	defaultDeployInstallTimeout = 20 * time.Minute
	defaultDeployTestLimit      = 20
	defaultDeployTestWindow     = 10 * time.Minute
	defaultDeployActionLimit    = 60
	defaultDeployActionWindow   = 10 * time.Minute
	defaultDeployMaxBatch       = 50
	// 公开证书（docs/03 §5.22，Ruling-74）：整条任务 10 分钟，每 12 小时检查一次哪些主机到了续期时间。
	defaultACMEDirectoryURL  = "https://acme-v02.api.letsencrypt.org/directory"
	defaultACMETimeout       = 10 * time.Minute
	defaultACMERenewInterval = 12 * time.Hour
	defaultCloudflareAPIURL  = "https://api.cloudflare.com/client/v4"

	// 公告 / 教程图片上传，语义见 docs/03 §5.9（Ruling-89）：单张 5 MiB，每个管理员每小时 60 张
	defaultUploadEndpoint = "http://minio:9000"
	defaultUploadBucket   = "uploads"
	defaultUploadRegion   = "us-east-1"
	defaultUploadTimeout  = 30 * time.Second
	defaultUploadMaxBytes = 5 << 20
	defaultUploadLimit    = 60
	defaultUploadWindow   = time.Hour
)

// ErrInvalidConfig 表示配置内容不合法。
var ErrInvalidConfig = errors.New("invalid config")

// Config 是 hub-server 的完整配置。
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Auth     AuthConfig     `yaml:"auth"`
	Panel    PanelConfig    `yaml:"panel"`
	User     UserConfig     `yaml:"user"`
	Portal   PortalConfig   `yaml:"portal"`
	Chain    ChainConfig    `yaml:"chain"`
	GeoIP    GeoIPConfig    `yaml:"geoip"`
	// Analytics 是总览分析（docs/03 §5.20，Ruling-66）；Reach 是大陆连通检测（Ruling-67）。
	Analytics AnalyticsConfig `yaml:"analytics"`
	Reach     ReachConfig     `yaml:"reach"`
	// Deploy 是 SSH 托管与面板部署（docs/03 §5.22）。
	Deploy DeployConfig `yaml:"deploy"`
	// Subscription 是订阅设置（docs/03 §5.4）；M4 起移入系统设置表。
	Subscription SubscriptionConfig `yaml:"subscription"`
	// Upload 是公告 / 教程图片上传（docs/03 §5.9，Ruling-89）。
	Upload UploadConfig `yaml:"upload"`
}

// UploadConfig 是图片上传：Endpoint / Bucket / Region 指向 S3 兼容存储（生产为同一 compose 里的 MinIO），
// MaxBytes 是单张上限，Limit / Window 是每个管理员的上传频率。账号只走环境变量，不在这里。
type UploadConfig struct {
	Endpoint string        `yaml:"endpoint"`
	Bucket   string        `yaml:"bucket"`
	Region   string        `yaml:"region"`
	Timeout  time.Duration `yaml:"timeout"`
	MaxBytes int64         `yaml:"maxBytes"`
	Limit    int           `yaml:"limit"`
	Window   time.Duration `yaml:"window"`
}

// ChainConfig 是服务端链式下发的节奏（docs/03 §5.19）：ApplyInterval 是后台检查间隔，
// MinApplyInterval 是同一台服务器两次下发的最小间隔（合并短时间内的多次改动；不会热应用的面板每次下发还要重启内核，Ruling-93）；
// UserWriteLimit / UserWriteWindow 是每个用户对服务端规则的写操作频率（Ruling-49）；
// UserTestLimit / UserTestWindow 是每个用户发起出口延迟测试的频率（Ruling-52）。
type ChainConfig struct {
	ApplyInterval    time.Duration `yaml:"applyInterval"`
	MinApplyInterval time.Duration `yaml:"minApplyInterval"`
	UserWriteLimit   int           `yaml:"userWriteLimit"`
	UserWriteWindow  time.Duration `yaml:"userWriteWindow"`
	UserTestLimit    int           `yaml:"userTestLimit"`
	UserTestWindow   time.Duration `yaml:"userTestWindow"`
}

// GeoIPConfig 是服务器位置扫描用的 IP 库（docs/03 §5.1，Ruling-65）。
// LookupURL 含 {ip} 占位，显式写成空串即关闭扫描。
type GeoIPConfig struct {
	LookupURL string        `yaml:"lookupUrl"`
	Timeout   time.Duration `yaml:"timeout"`
}

// AnalyticsConfig 是总览分析：CacheTTL 是同一分区同一参数的结果缓存多久，0 = 不缓存。
type AnalyticsConfig struct {
	CacheTTL time.Duration `yaml:"cacheTtl"`
}

// ReachConfig 是大陆连通检测用的探测服务（Globalping）：APIURL 是接口地址，Timeout 是一次检测的总时限，
// CheckInterval 是后台任务的检查周期。令牌只走环境变量；功能开关与检测间隔在系统设置里。
type ReachConfig struct {
	APIURL        string        `yaml:"apiUrl"`
	Timeout       time.Duration `yaml:"timeout"`
	CheckInterval time.Duration `yaml:"checkInterval"`
}

// DeployConfig 是 SSH 托管与面板部署（docs/03 §5.22）：Concurrency 是同时执行的主机任务数，PollInterval 是后台领取排队任务的间隔；
// ConnectTimeout 是 SSH 连接加握手的时限，CommandTimeout 是普通动作的总时限，InstallTimeout 是安装与升级的总时限；
// TestLimit / TestWindow 是每个管理员做连接测试的频率，ActionLimit / ActionWindow 是每个管理员发起动作的频率；
// MaxBatch 是一次批量新建的主机数上限；AllowLoopback 允许连回环地址，只给联调用，生产必须为 false。
// CloudflareAPIURL 是 Cloudflare 接口的根地址（只在测试里改）；ACME 是公开证书的签发参数。
type DeployConfig struct {
	Concurrency    int           `yaml:"concurrency"`
	PollInterval   time.Duration `yaml:"pollInterval"`
	ConnectTimeout time.Duration `yaml:"connectTimeout"`
	CommandTimeout time.Duration `yaml:"commandTimeout"`
	InstallTimeout time.Duration `yaml:"installTimeout"`
	TestLimit      int           `yaml:"testLimit"`
	TestWindow     time.Duration `yaml:"testWindow"`
	ActionLimit    int           `yaml:"actionLimit"`
	ActionWindow   time.Duration `yaml:"actionWindow"`
	MaxBatch       int           `yaml:"maxBatch"`
	AllowLoopback  bool          `yaml:"allowLoopback"`

	CloudflareAPIURL string     `yaml:"cloudflareApiUrl"`
	ACME             ACMEConfig `yaml:"acme"`
}

// ACMEConfig 是面板公开证书的签发参数（docs/03 §5.22，Ruling-74）：DirectoryURL 是签发方的目录地址，传给主机上的 acme.sh；
// Timeout 是一条“配置证书”任务的总时限；RenewInterval 是检查哪些主机到了续期时间的间隔（续期时间由 acme.sh 给出）。
type ACMEConfig struct {
	DirectoryURL  string        `yaml:"directoryUrl"`
	Timeout       time.Duration `yaml:"timeout"`
	RenewInterval time.Duration `yaml:"renewInterval"`
}

// SubscriptionConfig 是订阅的系统级设置。
type SubscriptionConfig struct {
	// BaseURL 是对外的用户域名根地址（如 https://user.example.com）；为空时用请求自身的地址。
	BaseURL string `yaml:"baseUrl"`
	// Title 是客户端里显示的订阅名称。
	Title               string `yaml:"title"`
	UpdateIntervalHours int    `yaml:"updateIntervalHours"`
	// InfoNodes 为 true 时在节点前附“剩余流量”“到期时间”两条说明节点。
	InfoNodes    bool          `yaml:"infoNodes"`
	LogRetention time.Duration `yaml:"logRetention"`
}

// PortalConfig 是终端用户会话参数（docs/03 §5.3）；登录频控、Cookie 的 Secure 与会话顺延节流沿用 auth 段。
type PortalConfig struct {
	SessionIdleTimeout time.Duration `yaml:"sessionIdleTimeout"`
	SessionMaxAge      time.Duration `yaml:"sessionMaxAge"`
	// LoginLogRetention 是登录记录的保留时长。
	LoginLogRetention time.Duration `yaml:"loginLogRetention"`
}

// UserConfig 是用量采集、对账兜底与配额时区参数（docs/03 §5.2）。
type UserConfig struct {
	TrafficInterval   time.Duration `yaml:"trafficInterval"`
	ReconcileInterval time.Duration `yaml:"reconcileInterval"`
	// TimeZone 是自动续期与周期重置用的 IANA 时区名，Local 表示服务器本地时区。
	TimeZone string `yaml:"timeZone"`
}

// Location 解析 TimeZone。
func (u UserConfig) Location() (*time.Location, error) {
	loc, err := time.LoadLocation(u.TimeZone)
	if err != nil {
		return nil, fmt.Errorf("%w: user.timeZone %q: %v", ErrInvalidConfig, u.TimeZone, err)
	}
	return loc, nil
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

// DatabaseConfig 是 PostgreSQL 连接池参数。连接串（含密码）只从环境变量 HUB_DATABASE_URL 读（Ruling-2）。
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
	}, User: UserConfig{
		TrafficInterval:   defaultUserTrafficInterval,
		ReconcileInterval: defaultUserReconcileInterval,
		TimeZone:          defaultUserTimeZone,
	}, Chain: ChainConfig{
		ApplyInterval:    defaultChainApplyInterval,
		MinApplyInterval: defaultChainMinApplyInterval,
		UserWriteLimit:   defaultChainUserWriteLimit,
		UserWriteWindow:  defaultChainUserWriteWindow,
		UserTestLimit:    defaultChainUserTestLimit,
		UserTestWindow:   defaultChainUserTestWindow,
	}, Portal: PortalConfig{
		SessionIdleTimeout: defaultPortalSessionIdleTimeout,
		SessionMaxAge:      defaultPortalSessionMaxAge,
		LoginLogRetention:  defaultPortalLoginLogRetention,
	}, GeoIP: GeoIPConfig{
		LookupURL: defaultGeoIPLookupURL,
		Timeout:   defaultGeoIPTimeout,
	}, Analytics: AnalyticsConfig{
		CacheTTL: defaultAnalyticsCacheTTL,
	}, Upload: UploadConfig{
		Endpoint: defaultUploadEndpoint,
		Bucket:   defaultUploadBucket,
		Region:   defaultUploadRegion,
		Timeout:  defaultUploadTimeout,
		MaxBytes: defaultUploadMaxBytes,
		Limit:    defaultUploadLimit,
		Window:   defaultUploadWindow,
	}, Reach: ReachConfig{
		APIURL:        defaultReachAPIURL,
		Timeout:       defaultReachTimeout,
		CheckInterval: defaultReachCheckInterval,
	}, Deploy: DeployConfig{
		Concurrency:    defaultDeployConcurrency,
		PollInterval:   defaultDeployPollInterval,
		ConnectTimeout: defaultDeployConnectTimeout,
		CommandTimeout: defaultDeployCommandTimeout,
		InstallTimeout: defaultDeployInstallTimeout,
		TestLimit:      defaultDeployTestLimit,
		TestWindow:     defaultDeployTestWindow,
		ActionLimit:    defaultDeployActionLimit,
		ActionWindow:   defaultDeployActionWindow,
		MaxBatch:       defaultDeployMaxBatch,

		CloudflareAPIURL: defaultCloudflareAPIURL,
		ACME: ACMEConfig{
			DirectoryURL: defaultACMEDirectoryURL, Timeout: defaultACMETimeout, RenewInterval: defaultACMERenewInterval,
		},
	}, Subscription: SubscriptionConfig{
		Title:               defaultSubscriptionTitle,
		UpdateIntervalHours: defaultSubscriptionIntervalHours,
		InfoNodes:           true,
		LogRetention:        defaultSubscribeLogRetention,
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
	if c.User.TrafficInterval <= 0 || c.User.ReconcileInterval <= 0 {
		return fmt.Errorf("%w: user intervals must be positive", ErrInvalidConfig)
	}
	if _, err := c.User.Location(); err != nil {
		return err
	}
	if c.Chain.ApplyInterval <= 0 || c.Chain.MinApplyInterval <= 0 {
		return fmt.Errorf("%w: chain intervals must be positive", ErrInvalidConfig)
	}
	if c.Chain.UserWriteLimit <= 0 || c.Chain.UserWriteWindow <= 0 {
		return fmt.Errorf("%w: chain user write limit and window must be positive", ErrInvalidConfig)
	}
	if c.Chain.UserTestLimit <= 0 || c.Chain.UserTestWindow <= 0 {
		return fmt.Errorf("%w: chain user test limit and window must be positive", ErrInvalidConfig)
	}
	if c.GeoIP.Timeout <= 0 {
		return fmt.Errorf("%w: geoip.timeout must be positive", ErrInvalidConfig)
	}
	if c.Analytics.CacheTTL < 0 {
		return fmt.Errorf("%w: analytics.cacheTtl must not be negative", ErrInvalidConfig)
	}
	if c.Reach.APIURL == "" || c.Reach.Timeout <= 0 || c.Reach.CheckInterval <= 0 {
		return fmt.Errorf("%w: reach.apiUrl, reach.timeout and reach.checkInterval must be set", ErrInvalidConfig)
	}
	dc := c.Deploy
	if dc.Concurrency <= 0 || dc.PollInterval <= 0 || dc.ConnectTimeout <= 0 || dc.CommandTimeout <= 0 || dc.InstallTimeout <= 0 {
		return fmt.Errorf("%w: deploy concurrency, interval and timeouts must be positive", ErrInvalidConfig)
	}
	if dc.TestLimit <= 0 || dc.TestWindow <= 0 || dc.ActionLimit <= 0 || dc.ActionWindow <= 0 || dc.MaxBatch <= 0 {
		return fmt.Errorf("%w: deploy limits and windows must be positive", ErrInvalidConfig)
	}
	ac := dc.ACME
	if dc.CloudflareAPIURL == "" || ac.DirectoryURL == "" || ac.Timeout <= 0 || ac.RenewInterval <= 0 {
		return fmt.Errorf("%w: deploy.cloudflareApiUrl, deploy.acme.directoryUrl, timeout and renewInterval must be set", ErrInvalidConfig)
	}
	uc := c.Upload
	if uc.Endpoint == "" || uc.Bucket == "" || uc.Timeout <= 0 || uc.MaxBytes <= 0 || uc.Limit <= 0 || uc.Window <= 0 {
		return fmt.Errorf("%w: upload endpoint, bucket, timeout, maxBytes, limit and window must be set", ErrInvalidConfig)
	}
	po := c.Portal
	if po.SessionIdleTimeout <= a.SessionTouchInterval || po.SessionMaxAge < po.SessionIdleTimeout || po.LoginLogRetention <= 0 {
		return fmt.Errorf("%w: portal session durations out of range", ErrInvalidConfig)
	}
	sc := c.Subscription
	if sc.Title == "" || sc.UpdateIntervalHours <= 0 || sc.LogRetention <= 0 {
		return fmt.Errorf("%w: subscription title, updateIntervalHours and logRetention must be set", ErrInvalidConfig)
	}
	if sc.BaseURL != "" {
		u, err := url.Parse(sc.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w: subscription.baseUrl must be an http(s) URL", ErrInvalidConfig)
		}
	}
	return nil
}
