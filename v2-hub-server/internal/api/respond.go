package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"v2-hub-server/internal/service"
)

const (
	codeOK            = "0"
	msgOK             = "ok"
	msgInternal       = "服务器内部错误"
	msgBadJSON        = "请求体不是合法的 JSON 或含未知字段"
	msgBadID          = "路径中的 id 不合法"
	msgBadPage        = "分页参数不合法"
	maxRequestBodyLen = 1 << 20 // 1 MiB
	queryPage         = "page"
	queryPageSize     = "pageSize"
	pathParamID       = "id"
)

// envelope 是业务接口统一响应信封（hub-api.yaml components.schemas.Envelope）。
type envelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// statusByCode 把业务错误码映射为 HTTP 状态，与 docs/03 §5 逐行对应。
var statusByCode = map[service.Code]int{
	service.CodeInvalidArgument:  http.StatusBadRequest,
	service.CodeUnauthenticated:  http.StatusUnauthorized,
	service.CodePermissionDenied: http.StatusForbidden,
	service.CodeNotFound:         http.StatusNotFound,
	service.CodeConflict:         http.StatusConflict,
	service.CodeRateLimited:      http.StatusTooManyRequests,
}

func writeOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, envelope{Code: codeOK, Message: msgOK, Data: data})
}

// writeError 输出业务错误；非 *service.Error 的错误一律记日志并返回 500，不向客户端透出细节。
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var se *service.Error
	if errors.As(err, &se) {
		if status, ok := statusByCode[se.Code]; ok {
			writeJSON(w, status, envelope{Code: string(se.Code), Message: se.Message})
			return
		}
	}
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeJSON(w, http.StatusInternalServerError, envelope{Code: string(service.CodeInternal), Message: msgInternal})
}

func badRequest(message string) error {
	return &service.Error{Code: service.CodeInvalidArgument, Message: message}
}

// decodeJSON 限制请求体大小并拒绝未知字段。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyLen)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return badRequest(msgBadJSON)
	}
	if dec.More() {
		return badRequest(msgBadJSON)
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(pathParamID), 10, 64)
	if err != nil || id < 1 {
		return 0, badRequest(msgBadID)
	}
	return id, nil
}

// pageQuery 解析分页参数；范围校验在 service 层统一做。
func pageQuery(r *http.Request) (service.PageQuery, error) {
	var pq service.PageQuery
	for name, dst := range map[string]*int{queryPage: &pq.Page, queryPageSize: &pq.PageSize} {
		raw := r.URL.Query().Get(name)
		if raw == "" {
			continue
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			return pq, badRequest(msgBadPage)
		}
		*dst = v
	}
	return pq, nil
}

// clientIP 取客户端地址：直连地址在可信代理网段内时，才采信 X-Forwarded-For 最右一个地址（Caddy 追加的那个）。
func clientIP(r *http.Request, trusted []netip.Prefix) *netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return nil
	}
	addr := ap.Addr().Unmap()
	if !inPrefixes(addr, trusted) {
		return &addr
	}
	if fwd := lastForwardedFor(r.Header.Values(headerForwardedFor)); fwd.IsValid() {
		return &fwd
	}
	return &addr
}

const headerForwardedFor = "X-Forwarded-For"

func inPrefixes(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func lastForwardedFor(values []string) netip.Addr {
	if len(values) == 0 {
		return netip.Addr{}
	}
	last := values[len(values)-1]
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(last))
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

// ParseTrustedProxies 解析可信代理网段配置。
func ParseTrustedProxies(cidrs []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("parse trusted proxy %q: %w", c, err)
		}
		out = append(out, p)
	}
	return out, nil
}
