package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 订阅地址的 token 是凭据：不论前缀是旧的 sub 还是随机前缀（Ruling-59），写日志的路径里都不能带。
func TestPathForLogMasksSubscriptionToken(t *testing.T) {
	const token = "Zm9vYmFyYmF6cXV4Zm9vYmFyYmF6cXV4"
	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "random prefix", path: "/0kvdss41n9olto30/" + token, want: "/0kvdss41n9olto30/***"},
		{name: "legacy prefix", path: "/sub/" + token, want: "/sub/***"},
		{name: "api path stays", path: "/api/user/subscription", want: "/api/user/subscription"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			mux := http.NewServeMux()
			mux.HandleFunc("GET /{subPath}/{token}", func(_ http.ResponseWriter, r *http.Request) { got = pathForLog(r) })
			mux.HandleFunc("GET /api/user/subscription", func(_ http.ResponseWriter, r *http.Request) { got = pathForLog(r) })
			mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, tc.path, nil))
			if got != tc.want {
				t.Fatalf("pathForLog(%q) = %q, want %q", tc.path, got, tc.want)
			}
			if strings.Contains(got, token) {
				t.Fatalf("pathForLog(%q) leaks the token", tc.path)
			}
		})
	}
}
