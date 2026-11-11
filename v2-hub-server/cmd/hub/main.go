// Command hub 启动 v2-hub 的后端服务：管理端 / 客户端 API 与订阅端点。
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"v2-hub-server/internal/api"
	"v2-hub-server/internal/config"
)

const defaultConfigPath = "etc/hub-server.yaml"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	configPath := flag.String("f", defaultConfigPath, "config file path")
	flag.Parse()

	if err := run(*configPath); err != nil {
		slog.Error("hub-server exited", "err", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           api.NewRouter(),
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
