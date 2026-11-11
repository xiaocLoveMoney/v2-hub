// Package api 是 hub-server 的 Web 层：路由注册、参数基础校验与响应编码。
// 业务逻辑放 service 层，这里只做转发（见 .claude/rules/architecture.md）。
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

const (
	pathHealthz     = "GET /healthz"
	headerContent   = "Content-Type"
	contentTypeJSON = "application/json; charset=utf-8"
	statusOK        = "ok"
)

// NewRouter 返回注册好全部路由的 http.Handler。
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(pathHealthz, handleHealthz)
	return mux
}

type healthResponse struct {
	Status string `json:"status"`
}

// handleHealthz 是存活探针：只说明进程在跑，不探测数据库等依赖。
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: statusOK})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set(headerContent, contentTypeJSON)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// 状态码已写出，只能记录；常见原因是客户端提前断开。
		slog.Warn("write json response", "status", status, "err", err)
	}
}
