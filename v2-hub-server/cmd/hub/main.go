// Command hub 启动 v2-hub 的后端服务：管理端 / 客户端 API 与订阅端点。
//
// 用法：
//
//	hub [-f 配置文件]                                       启动服务
//	hub admin-create [-f 配置文件] -username 名称            创建超级管理员（密码从终端无回显读取，或从标准输入读一行）；
//	                                                         库里还没有所有者时它即所有者
//	hub admin-owner-set [-f 配置文件] -username 名称         指定或转移所有者（目标须是启用状态的超级管理员）
//	hub admin-totp-reset [-f 配置文件] -username 名称        清除两步验证并使其全部会话失效
//	hub admin-password-reset [-f 配置文件] -username 名称    重设密码（读法同 admin-create）并使其全部会话失效
//
// admin-* 子命令只能在服务器上执行、不经 HTTP，审计的操作者记为系统（docs/03 §4.3 命令行）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/api"
	"v2-hub-server/internal/config"
	"v2-hub-server/internal/dao"
	"v2-hub-server/internal/job"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/deploy"
	deployxui "v2-hub-server/internal/manager/deploy/xui"
	"v2-hub-server/internal/manager/dns"
	"v2-hub-server/internal/manager/dns/cloudflare"
	"v2-hub-server/internal/manager/geoip"
	"v2-hub-server/internal/manager/mail"
	"v2-hub-server/internal/manager/objectstore"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/panel/xui"
	"v2-hub-server/internal/manager/pay"
	"v2-hub-server/internal/manager/reach"
	"v2-hub-server/internal/manager/remote"
	"v2-hub-server/internal/manager/telegram"
	"v2-hub-server/internal/service"
)

const (
	defaultConfigPath = "etc/hub-server.yaml"
	// envDatabaseURL 是 PostgreSQL 连接串（含密码）的环境变量名，不写进配置文件（Ruling-2）。
	envDatabaseURL = "HUB_DATABASE_URL"
	// envEncryptionKey 是 AES-256-GCM 主密钥（Base64 的 32 字节），加密 TOTP 密钥等（docs/03 §4.3）。
	envEncryptionKey = "HUB_ENCRYPTION_KEY"
	// envInsecureDevCookie=1 时关闭会话 Cookie 的 Secure 属性，只供本机 HTTP 开发与联调。
	envInsecureDevCookie = "HUB_INSECURE_DEV_COOKIE"
	// SMTP 配置只从环境变量读（Ruling-24）；主机或发件人为空即邮件不可用。
	envSMTPHost     = "HUB_SMTP_HOST"
	envSMTPPort     = "HUB_SMTP_PORT"
	envSMTPUsername = "HUB_SMTP_USERNAME"
	envSMTPSecret   = "HUB_SMTP_PASSWORD"
	envSMTPFrom     = "HUB_SMTP_FROM"
	envSMTPTLS      = "HUB_SMTP_TLS"
	// mailOutboxInterval 是发件箱任务的周期。
	mailOutboxInterval = 5 * time.Second
	// Telegram 机器人只走环境变量（docs/03 §5.20，Ruling-42）；API 地址可换成联调用的模拟端
	envTelegramToken   = "HUB_TELEGRAM_BOT_TOKEN"
	envTelegramAPIBase = "HUB_TELEGRAM_API_BASE"
	// envGeoIPLookupURL 覆盖配置里的 IP 库查询地址（联调指向模拟端；设为空串即关闭扫描，Ruling-65）
	envGeoIPLookupURL = "HUB_GEOIP_LOOKUP_URL"
	// envReachToken 是大陆连通检测用的探测服务令牌（可空，Ruling-67）；envReachAPIURL 覆盖配置里的接口地址（联调指向模拟端）
	envReachToken  = "HUB_GLOBALPING_TOKEN"
	envReachAPIURL = "HUB_REACH_API_URL"
	// 公告 / 教程图片上传用的 MinIO 只写账号（Ruling-89），缺任一即上传不可用；envUploadEndpoint 覆盖配置里的地址（本机开发指向 :19000）
	envUploadUser     = "HUB_UPLOAD_USER"
	envUploadSecret   = "HUB_UPLOAD_PASSWORD"
	envUploadEndpoint = "HUB_UPLOAD_ENDPOINT"
	// serverLocateInterval 是位置为空的服务器的补扫周期（docs/03 §5.1）。
	serverLocateInterval = time.Hour
	// telegramOutboxInterval 是 Telegram 发件箱任务的周期；opsInterval 是掉线告警与日报的检查周期。
	telegramOutboxInterval = 10 * time.Second
	opsInterval            = time.Minute
	// 面板重启后等它回来：最多探测 panelWaitAttempts 次，每次间隔 panelWaitInterval（docs/03 §5.22）。
	panelWaitAttempts = 15
	panelWaitInterval = 2 * time.Second

	cmdAdminCreate        = "admin-create"
	cmdAdminOwnerSet      = "admin-owner-set"
	cmdAdminTOTPReset     = "admin-totp-reset"
	cmdAdminPasswordReset = "admin-password-reset"
	flagConfig            = "f"
	flagConfigUsage       = "config file path"
	flagUsername          = "username"
	flagUsernameUsage     = "admin username"
)

// subcommands 是 hub 的子命令；第一个参数不是子命令名时按启动服务处理（参数原样交给 runServer）。
var subcommands = map[string]func(args []string) error{
	cmdAdminCreate:        runAdminCreate,
	cmdAdminOwnerSet:      runAdminOwnerSet,
	cmdAdminTOTPReset:     runAdminTOTPReset,
	cmdAdminPasswordReset: runAdminPasswordReset,
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	var err error
	var run func([]string) error
	if len(os.Args) > 1 {
		run = subcommands[os.Args[1]]
	}
	if run != nil {
		err = run(os.Args[2:])
	} else {
		err = runServer(os.Args[1:])
	}
	if err != nil {
		slog.Error("hub exited", "err", err)
		os.Exit(1)
	}
}

func loadConfigAndPool(ctx context.Context, configPath string) (config.Config, *pgxpool.Pool, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return config.Config{}, nil, err
	}
	pool, err := dao.NewPool(ctx, os.Getenv(envDatabaseURL), dao.PoolConfig{MaxConns: cfg.Database.MaxConns})
	if err != nil {
		return config.Config{}, nil, fmt.Errorf("init database (env %s): %w", envDatabaseURL, err)
	}
	return cfg, pool, nil
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	configPath := fs.String(flagConfig, defaultConfigPath, flagConfigUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, pool, err := loadConfigAndPool(ctx, *configPath)
	if err != nil {
		return err
	}
	defer pool.Close()

	trusted, err := api.ParseTrustedProxies(cfg.Server.TrustedProxies)
	if err != nil {
		return err
	}
	sealer, err := crypto.NewSealer(os.Getenv(envEncryptionKey))
	if err != nil {
		return fmt.Errorf("init encryption (env %s): %w", envEncryptionKey, err)
	}
	auth, err := service.NewAuthService(pool, toAuthConfig(cfg.Auth), sealer, nil)
	if err != nil {
		return err
	}
	secureCookie := cfg.Auth.SecureCookie && os.Getenv(envInsecureDevCookie) != "1"
	if !secureCookie {
		slog.Warn("session cookie Secure flag disabled; use only for local HTTP development")
	}

	go job.RunSessionCleanup(ctx, auth, cfg.Auth.SessionCleanupInterval)
	portalAuth, err := service.NewPortalAuthService(pool, toPortalAuthConfig(cfg), sealer, nil)
	if err != nil {
		return err
	}
	go job.RunSessionCleanup(ctx, portalAuth, cfg.Auth.SessionCleanupInterval)

	// 面板实现注册表：新增面板类型（如 S-UI）只在这里加一行（Ruling-8）
	registry := panel.NewRegistry(map[string]panel.Factory{xui.ProviderType: xui.New})
	locator, err := geoLocatorFromConfig(cfg.GeoIP)
	if err != nil {
		return err
	}
	subCfg := service.SubscriptionConfig{
		BaseURL: cfg.Subscription.BaseURL, Title: cfg.Subscription.Title, UpdateIntervalHours: cfg.Subscription.UpdateIntervalHours,
		InfoNodes: cfg.Subscription.InfoNodes, LogRetention: cfg.Subscription.LogRetention,
	}
	settings := service.NewSettingsService(pool, subCfg, nil)
	// 订阅路径前缀首次启动随机生成，老部署的 sub 进入过渡期（Ruling-59）
	if err := settings.EnsureSubscriptionPath(ctx); err != nil {
		return fmt.Errorf("ensure subscription path: %w", err)
	}
	// 流量周期与到期日按站点时区取“今天”（Ruling-84）、趋势是否带延迟看开关（Ruling-86），所以面板服务要读系统设置
	panels := service.NewPanelService(pool, sealer, registry, service.PanelConfig{
		RequestTimeout: cfg.Panel.RequestTimeout, Locator: locator, SiteLocation: settings.SiteLocation,
		LatencyEnabled: settings.LatencyEnabled,
	}, nil)
	go job.RunPanelPolling(ctx, panels, job.PanelPollConfig{
		ProbeInterval: cfg.Panel.ProbeInterval, SyncInterval: cfg.Panel.SyncInterval, Concurrency: cfg.Panel.Concurrency,
	})
	quotaLoc, err := cfg.User.Location()
	if err != nil {
		return err
	}
	go job.RunSessionCleanup(ctx, job.PurgeFunc(panels.PurgeServerMetrics), cfg.Auth.SessionCleanupInterval)
	// 位置为空的服务器在后台补扫：启动时一次、之后每小时一次，不阻塞启动（Ruling-65）
	go job.RunServerLocate(ctx, panels, serverLocateInterval)
	// 节点旧默认名去掉服务器名前缀（Ruling-63）
	if _, err := panels.RenameLegacyNodes(ctx); err != nil {
		return fmt.Errorf("rename legacy nodes: %w", err)
	}
	sender, err := smtpSenderFromEnv()
	if err != nil {
		return err
	}
	mails := service.NewMailService(pool, sealer, sender, settings, nil)
	go job.RunMailOutbox(ctx, mails, mailOutboxInterval)
	telegramSender, err := telegramSenderFromEnv()
	if err != nil {
		return err
	}
	telegramSvc := service.NewTelegramService(pool, telegramSender, settings, nil)
	go job.RunTelegramOutbox(ctx, telegramSvc, telegramOutboxInterval)
	go job.RunSessionCleanup(ctx, job.PurgeFunc(telegramSvc.PurgeOutbox), cfg.Auth.SessionCleanupInterval)
	ops := service.NewOpsService(pool, settings, mails, telegramSvc, nil)
	go job.RunOps(ctx, ops, opsInterval)
	go job.RunSessionCleanup(ctx, job.PurgeFunc(mails.PurgeOutbox), cfg.Auth.SessionCleanupInterval)
	// 大陆连通检测（Ruling-67）：开关在系统设置里，默认关；关着时这个任务每轮什么都不做
	reachToken := os.Getenv(envReachToken)
	prober, err := reachProberFromConfig(cfg.Reach, reachToken)
	if err != nil {
		return err
	}
	reachSvc := service.NewReachService(pool, settings, ops, prober, service.ReachOptions{HasToken: reachToken != ""})
	go job.RunReach(ctx, reachSvc, cfg.Reach.CheckInterval)
	// 三网延迟检测（Ruling-86）：与连通检测同一个探测服务、共用小时额度；开关在系统设置里，默认关
	go job.RunLatency(ctx, service.NewLatencyService(pool, settings, prober, reachSvc, nil), cfg.Reach.CheckInterval)
	analytics := service.NewAnalyticsService(pool, settings, reachSvc, cfg.Analytics.CacheTTL, nil)
	users := service.NewUserService(pool, sealer, panels, service.UserConfig{Location: quotaLoc, Subscription: subCfg, Settings: settings}, nil)
	// 老用户补 SS2022 密钥（Ruling-60），对账随后写到面板
	if _, err := users.EnsureSS2022Keys(ctx); err != nil {
		return fmt.Errorf("ensure ss2022 keys: %w", err)
	}
	account := service.NewAccountService(pool, sealer, service.AccountDeps{Settings: settings, Mail: mails, Users: users, Portal: portalAuth}, nil)
	shop := service.NewShopService(pool, settings, users, mails, nil)
	// M5 不带具体支付网关（Ruling-28）：注册表为空，在线充值入口不出现；接入网关时在这里注册，密钥走环境变量
	gateways, err := pay.NewRegistry()
	if err != nil {
		return err
	}
	payments := service.NewPayService(pool, settings, gateways, telegramSvc, nil)
	go job.RunSessionCleanup(ctx, job.PurgeFunc(payments.ExpirePayments), cfg.Auth.SessionCleanupInterval)
	// 用量任务每轮的“推进用户”换成商店的入口：先做到期的扣余额续费，再推进状态，最后发提醒邮件（docs/03 §5.16）
	go job.RunUserSync(ctx, billingSyncer{UserService: users, shop: shop}, job.UserSyncConfig{
		TrafficInterval: cfg.User.TrafficInterval, ReconcileInterval: cfg.User.ReconcileInterval, Concurrency: cfg.Panel.Concurrency,
	})

	go job.RunSessionCleanup(ctx, job.PurgeFunc(users.PurgeSubscribeLogs), cfg.Auth.SessionCleanupInterval)
	chainCfg := service.DefaultChainApplyConfig()
	chainCfg.Settings = settings
	chainCfg.MinInterval = cfg.Chain.MinApplyInterval
	chainApplier := service.NewChainApplier(pool, sealer, users, panels, chainCfg, nil)
	go job.RunChainApply(ctx, chainApplier, cfg.Chain.ApplyInterval)
	chains := service.NewChainService(pool, sealer, chainApplier, service.ChainOptions{
		ServerWriteLimit: cfg.Chain.UserWriteLimit, ServerWriteWindow: cfg.Chain.UserWriteWindow,
		ExitTestLimit: cfg.Chain.UserTestLimit, ExitTestWindow: cfg.Chain.UserTestWindow,
	})

	// SSH 托管与面板部署（docs/03 §5.22）：新增面板类型的部署实现只在这里加一行（Ruling-69）
	deployers := deploy.NewRegistry(map[string]deploy.Deployer{deployxui.ProviderType: deployxui.New()})
	// 公开证书：新增 DNS 服务商只在这里加一行（Ruling-72）；证书由主机上的 acme.sh 签发，签发方默认 Let's Encrypt（Ruling-74）
	acmeCfg := cfg.Deploy.ACME
	hosts := service.NewHostService(pool, sealer, service.HostDeps{
		Dialer:     remote.NewSSHDialer(remote.Config{ConnectTimeout: cfg.Deploy.ConnectTimeout, AllowLoopback: cfg.Deploy.AllowLoopback}),
		Deployers:  deployers,
		DNS:        dns.NewRegistry(map[string]dns.Factory{cloudflare.ProviderType: cloudflare.NewFactory(cloudflare.Config{BaseURL: cfg.Deploy.CloudflareAPIURL})}),
		Panels:     panels,
		Reconciler: users,
	},
		service.HostConfig{
			CommandTimeout: cfg.Deploy.CommandTimeout, InstallTimeout: cfg.Deploy.InstallTimeout,
			CertTimeout: acmeCfg.Timeout, ACMEDirectoryURL: acmeCfg.DirectoryURL,
			TestLimit: cfg.Deploy.TestLimit, TestWindow: cfg.Deploy.TestWindow,
			ActionLimit: cfg.Deploy.ActionLimit, ActionWindow: cfg.Deploy.ActionWindow,
			MaxBatch: cfg.Deploy.MaxBatch, AllowLoopback: cfg.Deploy.AllowLoopback,
			PanelWaitAttempts: panelWaitAttempts, PanelWaitInterval: panelWaitInterval,
			// 密码凭据的确认输错频控与登录同一组参数（docs/03 §5.22：15 分钟内 5 次锁 15 分钟）
			ConfirmMaxFailures: cfg.Auth.LoginMaxFailures, ConfirmWindow: cfg.Auth.LoginWindow, ConfirmLockout: cfg.Auth.LoginLockout,
		})
	go job.RunHostTasks(ctx, hosts, cfg.Deploy.PollInterval, cfg.Deploy.Concurrency)
	go job.RunCertRenewal(ctx, hosts, acmeCfg.RenewInterval)

	objects, err := uploadStoreFromEnv(cfg.Upload)
	if err != nil {
		return err
	}
	uploads := service.NewUploadService(pool, objects, service.UploadConfig{
		MaxBytes: cfg.Upload.MaxBytes, Limit: cfg.Upload.Limit, Window: cfg.Upload.Window,
	})

	tickets := service.NewTicketService(pool, settings, mails, telegramSvc, nil)
	// 二级代理（docs/03 §5.23）：写操作频控取配置 agent.writeRatePerHour（Ruling-104）
	agents := service.NewAgentService(pool, users, tickets, service.AgentServiceConfig{WriteRatePerHour: cfg.Agent.WriteRatePerHour})

	srv := &http.Server{
		Addr: cfg.Server.ListenAddr,
		Handler: api.NewRouter(api.Deps{
			Database:       pool,
			PingTimeout:    cfg.Database.PingTimeout,
			Auth:           auth,
			RBAC:           service.NewRBACService(pool),
			Panels:         panels,
			Users:          users,
			PortalAuth:     portalAuth,
			Portal:         users,
			Settings:       settings,
			Mail:           mails,
			Account:        account,
			Content:        service.NewContentService(pool, sealer, settings, mails, nil),
			Tickets:        tickets,
			Wallet:         service.NewWalletService(pool, sealer, settings, nil),
			Shop:           shop,
			Finance:        shop,
			Pay:            payments,
			Chain:          chains,
			ChainAdmin:     chains,
			ChainApply:     chainApplier,
			Telegram:       telegramSvc,
			Ops:            ops,
			Analytics:      analytics,
			Reach:          reachSvc,
			Hosts:          hosts,
			Uploads:        uploads,
			Agents:         agents,
			Cookie:         api.CookieConfig{Secure: secureCookie},
			TrustedProxies: trusted,
		}),
		ReadHeaderTimeout: cfg.Server.ReadTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("hub-server listening", "addr", cfg.Server.ListenAddr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	slog.Info("hub-server shutting down")
	return srv.Shutdown(shutdownCtx)
}

// uploadStoreFromEnv 按配置与环境变量创建图片存储；账号缺任一返回 nil（上传不可用），配置不合法时报错让进程启动失败。
func uploadStoreFromEnv(cfg config.UploadConfig) (objectstore.Store, error) {
	user, secret := os.Getenv(envUploadUser), os.Getenv(envUploadSecret)
	if user == "" || secret == "" {
		slog.Warn("image upload disabled: upload credentials not set", "env", envUploadUser)
		return nil, nil
	}
	endpoint := cfg.Endpoint
	if v := os.Getenv(envUploadEndpoint); v != "" {
		endpoint = v
	}
	client, err := objectstore.New(objectstore.Config{
		Endpoint: endpoint, Bucket: cfg.Bucket, Region: cfg.Region, AccessKey: user, SecretKey: secret, Timeout: cfg.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("init upload store: %w", err)
	}
	return client, nil
}

// smtpSenderFromEnv 按环境变量创建 SMTP 发送器；未配置时返回 nil（邮件不可用），配置不合法时报错让进程启动失败。
func smtpSenderFromEnv() (mail.Sender, error) {
	cfg := mail.Config{
		Host: os.Getenv(envSMTPHost), Username: os.Getenv(envSMTPUsername), Secret: os.Getenv(envSMTPSecret),
		From: os.Getenv(envSMTPFrom), TLS: os.Getenv(envSMTPTLS),
	}
	if raw := os.Getenv(envSMTPPort); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", envSMTPPort, err)
		}
		cfg.Port = port
	}
	sender, err := mail.NewSMTPSender(cfg)
	if errors.Is(err, mail.ErrDisabled) {
		slog.Warn("mail is not configured; email-dependent features are disabled", "hint", envSMTPHost+" / "+envSMTPFrom)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("init smtp (env %s*): %w", "HUB_SMTP_", err)
	}
	return sender, nil
}

func toAuthConfig(a config.AuthConfig) service.AuthConfig {
	return service.AuthConfig{
		SessionIdleTimeout:   a.SessionIdleTimeout,
		SessionMaxAge:        a.SessionMaxAge,
		SessionTouchInterval: a.SessionTouchInterval,
		LoginMaxFailures:     a.LoginMaxFailures,
		LoginWindow:          a.LoginWindow,
		LoginLockout:         a.LoginLockout,
		LoginMaxTrackedKeys:  a.LoginMaxTrackedKeys,
		ReauthWindow:         a.ReauthWindow,
	}
}

func toPortalAuthConfig(c config.Config) service.PortalAuthConfig {
	return service.PortalAuthConfig{
		SessionIdleTimeout:   c.Portal.SessionIdleTimeout,
		SessionMaxAge:        c.Portal.SessionMaxAge,
		SessionTouchInterval: c.Auth.SessionTouchInterval,
		LoginMaxFailures:     c.Auth.LoginMaxFailures,
		LoginWindow:          c.Auth.LoginWindow,
		LoginLockout:         c.Auth.LoginLockout,
		LoginMaxTrackedKeys:  c.Auth.LoginMaxTrackedKeys,
		LoginLogRetention:    c.Portal.LoginLogRetention,
	}
}

// billingSyncer 把用量任务里的“推进用户”交给 ShopService，其余能力仍由 UserService 提供。
type billingSyncer struct {
	*service.UserService
	shop *service.ShopService
}

// AdvanceUsers 见 service.ShopService.AdvanceUsers。
func (b billingSyncer) AdvanceUsers(ctx context.Context) (int, error) {
	return b.shop.AdvanceUsers(ctx)
}

// adminCommand 解析 admin-* 子命令的公共参数（-f、-username），连上数据库后执行 fn。
func adminCommand(name string, args []string, fn func(ctx context.Context, rbac *service.RBACService, username string) error) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	configPath := fs.String(flagConfig, defaultConfigPath, flagConfigUsage)
	username := fs.String(flagUsername, "", flagUsernameUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return fmt.Errorf("%s: -%s is required", name, flagUsername)
	}
	ctx := context.Background()
	_, pool, err := loadConfigAndPool(ctx, *configPath)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := fn(ctx, service.NewRBACService(pool), *username); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func runAdminCreate(args []string) error {
	return adminCommand(cmdAdminCreate, args, func(ctx context.Context, rbac *service.RBACService, username string) error {
		secret, err := readNewCredential(os.Stdin, os.Stderr)
		if err != nil {
			return err
		}
		admin, err := rbac.BootstrapSuperAdmin(ctx, username, secret)
		if err != nil {
			return err
		}
		slog.Info("super admin created", "id", admin.ID, "username", admin.Username, "is_owner", admin.IsOwner)
		return nil
	})
}

// runAdminOwnerSet 指定或转移所有者（R11，Ruling-99）。
func runAdminOwnerSet(args []string) error {
	return adminCommand(cmdAdminOwnerSet, args, func(ctx context.Context, rbac *service.RBACService, username string) error {
		previous, err := rbac.TransferOwner(ctx, username)
		if err != nil {
			return err
		}
		slog.Info("owner set", "username", username, "previous_owner_id", previous)
		return nil
	})
}

// runAdminTOTPReset 清除两步验证并吊销其全部会话（Ruling-98）。
func runAdminTOTPReset(args []string) error {
	return adminCommand(cmdAdminTOTPReset, args, func(ctx context.Context, rbac *service.RBACService, username string) error {
		if err := rbac.ResetAdminTOTPByUsername(ctx, username); err != nil {
			return err
		}
		slog.Info("admin totp reset", "username", username)
		return nil
	})
}

// runAdminPasswordReset 重设密码并吊销其全部会话（Ruling-98）；口令从终端读入，不经命令行参数。
func runAdminPasswordReset(args []string) error {
	return adminCommand(cmdAdminPasswordReset, args, func(ctx context.Context, rbac *service.RBACService, username string) error {
		secret, err := readNewCredential(os.Stdin, os.Stderr)
		if err != nil {
			return err
		}
		if err := rbac.ResetAdminPasswordByUsername(ctx, username, secret); err != nil {
			return err
		}
		slog.Info("admin password reset", "username", username)
		return nil
	})
}

// telegramSenderFromEnv 按环境变量创建 Telegram 发送器；没配 token 返回 nil（Telegram 不可用）。
// 注意返回接口类型的 nil，不能把 nil 的 *telegram.Client 装进接口。
func telegramSenderFromEnv() (telegram.Sender, error) {
	client, err := telegram.New(telegram.Config{Token: os.Getenv(envTelegramToken), APIBase: os.Getenv(envTelegramAPIBase)})
	if err != nil {
		return nil, fmt.Errorf("init telegram (env %s / %s): %w", envTelegramToken, envTelegramAPIBase, err)
	}
	if client == nil {
		return nil, nil
	}
	return client, nil
}

// reachProberFromConfig 按配置（环境变量可覆盖接口地址）创建大陆连通检测的探测客户端；token 可空。
func reachProberFromConfig(cfg config.ReachConfig, token string) (*reach.Client, error) {
	apiURL := cfg.APIURL
	if v := os.Getenv(envReachAPIURL); v != "" {
		apiURL = v
	}
	client, err := reach.New(reach.Config{APIURL: apiURL, Token: token, Timeout: cfg.Timeout})
	if err != nil {
		return nil, fmt.Errorf("init reach prober (env %s): %w", envReachAPIURL, err)
	}
	return client, nil
}

// geoLocatorFromConfig 按配置（环境变量可覆盖地址）创建 IP 库客户端；地址为空返回 nil，表示位置扫描关闭。
func geoLocatorFromConfig(cfg config.GeoIPConfig) (geoip.Locator, error) {
	lookupURL := cfg.LookupURL
	if v, ok := os.LookupEnv(envGeoIPLookupURL); ok {
		lookupURL = v
	}
	client, err := geoip.New(geoip.Config{LookupURL: lookupURL, Timeout: cfg.Timeout})
	if err != nil {
		return nil, err
	}
	if client == nil {
		slog.Info("server location scan disabled")
		return nil, nil
	}
	return client, nil
}
