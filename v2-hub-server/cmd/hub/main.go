// Command hub 启动 v2-hub 的后端服务：管理端 / 客户端 API 与订阅端点。
//
// 用法：
//
//	hub [-f 配置文件]                              启动服务
//	hub admin-create [-f 配置文件] -username 名称   创建超级管理员（口令从终端无回显读取，或从标准输入读一行）
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
	"v2-hub-server/internal/manager/mail"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/panel/xui"
	"v2-hub-server/internal/manager/pay"
	"v2-hub-server/internal/service"
)

const (
	defaultConfigPath = "etc/hub-server.yaml"
	// envDatabaseURL 是 PostgreSQL 连接串（含口令）的环境变量名，不写进配置文件（Ruling-2）。
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

	cmdAdminCreate  = "admin-create"
	flagConfig      = "f"
	flagConfigUsage = "config file path"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	var err error
	if len(os.Args) > 1 && os.Args[1] == cmdAdminCreate {
		err = runAdminCreate(os.Args[2:])
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
	panels := service.NewPanelService(pool, sealer, registry, service.PanelConfig{RequestTimeout: cfg.Panel.RequestTimeout}, nil)
	go job.RunPanelPolling(ctx, panels, job.PanelPollConfig{
		ProbeInterval: cfg.Panel.ProbeInterval, SyncInterval: cfg.Panel.SyncInterval, Concurrency: cfg.Panel.Concurrency,
	})
	quotaLoc, err := cfg.User.Location()
	if err != nil {
		return err
	}
	subCfg := service.SubscriptionConfig{
		BaseURL: cfg.Subscription.BaseURL, Title: cfg.Subscription.Title, UpdateIntervalHours: cfg.Subscription.UpdateIntervalHours,
		InfoNodes: cfg.Subscription.InfoNodes, LogRetention: cfg.Subscription.LogRetention,
	}
	settings := service.NewSettingsService(pool, subCfg, nil)
	sender, err := smtpSenderFromEnv()
	if err != nil {
		return err
	}
	mails := service.NewMailService(pool, sealer, sender, settings, nil)
	go job.RunMailOutbox(ctx, mails, mailOutboxInterval)
	go job.RunSessionCleanup(ctx, job.PurgeFunc(mails.PurgeOutbox), cfg.Auth.SessionCleanupInterval)
	users := service.NewUserService(pool, sealer, panels, service.UserConfig{Location: quotaLoc, Subscription: subCfg, Settings: settings}, nil)
	account := service.NewAccountService(pool, sealer, service.AccountDeps{Settings: settings, Mail: mails, Users: users, Portal: portalAuth}, nil)
	shop := service.NewShopService(pool, settings, users, mails, nil)
	// M5 不带具体支付网关（Ruling-28）：注册表为空，在线充值入口不出现；接入网关时在这里注册，密钥走环境变量
	gateways, err := pay.NewRegistry()
	if err != nil {
		return err
	}
	payments := service.NewPayService(pool, settings, gateways, nil)
	go job.RunSessionCleanup(ctx, job.PurgeFunc(payments.ExpirePayments), cfg.Auth.SessionCleanupInterval)
	// 用量任务每轮的“推进用户”换成商店的入口：先做到期的扣余额续费，再推进状态，最后发提醒邮件（docs/03 §5.16）
	go job.RunUserSync(ctx, billingSyncer{UserService: users, shop: shop}, job.UserSyncConfig{
		TrafficInterval: cfg.User.TrafficInterval, ReconcileInterval: cfg.User.ReconcileInterval, Concurrency: cfg.Panel.Concurrency,
	})

	go job.RunSessionCleanup(ctx, job.PurgeFunc(users.PurgeSubscribeLogs), cfg.Auth.SessionCleanupInterval)
	chainCfg := service.DefaultChainApplyConfig()
	chainCfg.MinInterval = cfg.Chain.MinApplyInterval
	chainApplier := service.NewChainApplier(pool, sealer, users, panels, chainCfg, nil)
	go job.RunChainApply(ctx, chainApplier, cfg.Chain.ApplyInterval)

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
			Tickets:        service.NewTicketService(pool, settings, mails, nil),
			Wallet:         service.NewWalletService(pool, sealer, settings, nil),
			Shop:           shop,
			Finance:        shop,
			Pay:            payments,
			Chain:          service.NewChainService(pool, sealer, chainApplier),
			ChainApply:     chainApplier,
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

func runAdminCreate(args []string) error {
	fs := flag.NewFlagSet(cmdAdminCreate, flag.ContinueOnError)
	configPath := fs.String(flagConfig, defaultConfigPath, flagConfigUsage)
	username := fs.String("username", "", "super admin username")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return errors.New("admin-create: -username is required")
	}
	secret, err := readNewCredential(os.Stdin, os.Stderr)
	if err != nil {
		return err
	}

	ctx := context.Background()
	_, pool, err := loadConfigAndPool(ctx, *configPath)
	if err != nil {
		return err
	}
	defer pool.Close()

	admin, err := service.NewRBACService(pool).BootstrapSuperAdmin(ctx, *username, secret)
	if err != nil {
		return fmt.Errorf("admin-create: %w", err)
	}
	slog.Info("super admin created", "id", admin.ID, "username", admin.Username)
	return nil
}
