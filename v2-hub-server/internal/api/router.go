// Package api 是 hub-server 的 Web 层：路由注册、参数基础校验与响应编码。
// 业务逻辑放 service 层，这里只做转发（见 .claude/rules/architecture.md）。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

const (
	pathHealthz       = "GET /healthz"
	pathReadyz        = "GET /readyz"
	headerContent     = "Content-Type"
	contentTypeJSON   = "application/json; charset=utf-8"
	statusOK          = "ok"
	statusUnavailable = "unavailable"
)

// Pinger 能探测某个依赖是否可用，*pgxpool.Pool 天然满足。
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps 是 Web 层需要的外部依赖。
type Deps struct {
	// Database 用于 /readyz 探测数据库，不可为 nil。
	Database Pinger
	// PingTimeout 是单次就绪探测的超时，必须为正。
	PingTimeout time.Duration
}

// NewRouter 返回注册好全部路由的 http.Handler。
func NewRouter(deps Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(pathHealthz, handleHealthz)
	mux.HandleFunc(pathReadyz, newReadyzHandler(deps.Database, deps.PingTimeout))
	return mux
}

type statusResponse struct {
	Status string `json:"status"`
}

// handleHealthz 是存活探针：只说明进程在跑，不探测数据库等依赖。
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{Status: statusOK})
}

// newReadyzHandler 返回就绪探针：数据库可达才返回 200，否则 503。
// 失败原因只写日志，不回给调用方（可能含内部地址）。
func newReadyzHandler(db Pinger, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			slog.Warn("readiness check failed", "dependency", "database", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, statusResponse{Status: statusUnavailable})
			return
		}
		writeJSON(w, http.StatusOK, statusResponse{Status: statusOK})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set(headerContent, contentTypeJSON)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// 状态码已写出，只能记录；常见原因是客户端提前断开。
		slog.Warn("write json response", "status", status, "err", err)
	}
}
