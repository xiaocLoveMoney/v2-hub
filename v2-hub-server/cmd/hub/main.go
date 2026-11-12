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
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/api"
	"v2-hub-server/internal/config"
	"v2-hub-server/internal/dao"
	"v2-hub-server/internal/job"
	"v2-hub-server/internal/manager/crypto"
	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/panel/xui"
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
	cmdAdminCreate       = "admin-create"
	flagConfig           = "f"
	flagConfigUsage      = "config file path"
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

	// 面板实现注册表：新增面板类型（如 S-UI）只在这里加一行（Ruling-8）
	registry := panel.NewRegistry(map[string]panel.Factory{xui.ProviderType: xui.New})
	panels := service.NewPanelService(pool, sealer, registry, service.PanelConfig{RequestTimeout: cfg.Panel.RequestTimeout}, nil)
	go job.RunPanelPolling(ctx, panels, job.PanelPollConfig{
		ProbeInterval: cfg.Panel.ProbeInterval, SyncInterval: cfg.Panel.SyncInterval, Concurrency: cfg.Panel.Concurrency,
	})

	srv := &http.Server{
		Addr: cfg.Server.ListenAddr,
		Handler: api.NewRouter(api.Deps{
			Database:       pool,
			PingTimeout:    cfg.Database.PingTimeout,
			Auth:           auth,
			RBAC:           service.NewRBACService(pool),
			Panels:         panels,
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
