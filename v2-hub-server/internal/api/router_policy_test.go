package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"v2-hub-server/internal/service"
)

// 路由归类测试：从 router.go 的源码里取出全部管理端路由及其鉴权选项，逐条核对归类（R1、R10、R12）。
// 目的是以后新增管理端路由时漏挂鉴权、漏挂二次验证或误挂豁免会让测试变红。新增路由时同步更新下面的清单。

const (
	routerSource   = "router.go"
	contractAPI    = "../../contract/hub-api.yaml"
	adminPrefix    = "/api/admin/"
	adminLoginPath = "POST /api/admin/auth/login"
	optFresh       = "withFresh"
	optSetupExempt = "withSetupExempt"
	optPerm        = "withPerm"
	// reauthRefSuffix 是 hub-api.yaml 里高危接口 403 引用的响应
	reauthRefSuffix = "/ReauthRequired"
)

// httpMethods 是 OpenAPI 路径项里表示操作的键。
var httpMethods = []string{"get", "post", "put", "delete", "patch"}

// loginOnlyRoutes 是只需登录、不查权限码的管理端路由（本人的会话与账号操作，以及按权限码自己裁剪区块的总览）。
var loginOnlyRoutes = []string{
	"POST /api/admin/auth/logout",
	"GET /api/admin/auth/me",
	"POST /api/admin/auth/totp/setup",
	"POST /api/admin/auth/totp/enable",
	"POST /api/admin/auth/totp/disable",
	"PUT /api/admin/auth/password",
	"POST /api/admin/auth/reauth",
	"GET /api/admin/dashboard",
	"GET /api/admin/analytics/backlog",
}

// setupExemptRoutes 是尚未开启两步验证的超管也能访问的路由（R10）。
var setupExemptRoutes = []string{
	"POST /api/admin/auth/logout",
	"GET /api/admin/auth/me",
	"POST /api/admin/auth/totp/setup",
	"POST /api/admin/auth/totp/enable",
	"PUT /api/admin/auth/password",
}

// freshRoutes 是 docs/03 §4.4 列出的高危接口（R12），须与 hub-api.yaml 里 403 引用 ReauthRequired 的接口一致。
var freshRoutes = []string{
	// ① 管理员与角色的全部写操作
	"POST /api/admin/roles", "PUT /api/admin/roles/{id}", "DELETE /api/admin/roles/{id}",
	"POST /api/admin/admins", "PUT /api/admin/admins/{id}", "PUT /api/admin/admins/{id}/password", "DELETE /api/admin/admins/{id}/totp",
	// ② 服务器的接入、修改、删除
	"POST /api/admin/servers", "PUT /api/admin/servers/{id}", "DELETE /api/admin/servers/{id}",
	// ③ 一键部署入站
	"POST /api/admin/servers/{id}/inbound-presets/preview", "POST /api/admin/servers/{id}/inbound-presets",
	// ④ SSH 托管的全部写操作
	"POST /api/admin/ssh-credentials", "PUT /api/admin/ssh-credentials/{id}", "DELETE /api/admin/ssh-credentials/{id}",
	"POST /api/admin/dns-accounts", "PUT /api/admin/dns-accounts/{id}", "DELETE /api/admin/dns-accounts/{id}",
	"POST /api/admin/server-hosts", "POST /api/admin/server-hosts/test", "PUT /api/admin/server-hosts/{id}", "DELETE /api/admin/server-hosts/{id}",
	"POST /api/admin/server-hosts/{id}/reset-host-key", "POST /api/admin/server-hosts/{id}/actions",
	"POST /api/admin/server-hosts/{id}/panel-login/reset", "POST /api/admin/host-tasks/{id}/cancel", "PUT /api/admin/servers/{id}/host",
	// ⑤ 查看密文
	"GET /api/admin/chain-exits/{id}/link", "GET /api/admin/inbounds/{id}/panel-config",
	"GET /api/admin/shared-accounts/{id}/credential", "GET /api/admin/topup-codes/{id}/code",
	// ⑥ 用户：删除、重置密码、清除两步验证、调整余额、切换身份
	"DELETE /api/admin/users/{id}", "PUT /api/admin/users/{id}/password", "DELETE /api/admin/users/{id}/totp",
	"POST /api/admin/users/{id}/balance", "POST /api/admin/users/{id}/impersonate",
	// ⑦ 生成充值码
	"POST /api/admin/topup-codes",
	// ⑧ 保存系统设置（含菜单顺序）
	"PUT /api/admin/settings",
}

// routeMeta 是从源码里读出的一条管理端路由的鉴权声明。
type routeMeta struct {
	perm        string // 权限码常量名（如 PermNodeWrite），空 = 只需登录
	extraPerm   string
	fresh       bool
	setupExempt bool
	public      bool // 不经 requireAdmin（只有登录）
}

// parseAdminRoutes 解析 router.go 里 NewRouter 注册的全部管理端路由。
func parseAdminRoutes(t *testing.T) map[string]routeMeta {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, routerSource, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", routerSource, err)
	}
	routes := map[string]routeMeta{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isSelector(call.Fun, "mux", "HandleFunc") || len(call.Args) != 2 {
			return true
		}
		handler := call.Args[1]
		lit, isLit := call.Args[0].(*ast.BasicLit)
		usesRequireAdmin := isRequireAdmin(handler)
		if !isLit {
			if usesRequireAdmin {
				t.Fatalf("%s: 管理端路由的 pattern 必须是字面量，测试才能核对", fset.Position(call.Pos()))
			}
			return true
		}
		pattern, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatalf("unquote %s: %v", lit.Value, err)
		}
		if !strings.Contains(pattern, " "+adminPrefix) {
			if usesRequireAdmin {
				t.Fatalf("%s 用了 requireAdmin 却不在 %s 下", pattern, adminPrefix)
			}
			return true
		}
		if _, dup := routes[pattern]; dup {
			t.Fatalf("duplicate route %s", pattern)
		}
		routes[pattern] = metaOf(t, pattern, handler)
		return true
	})
	return routes
}

func isSelector(e ast.Expr, x, sel string) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := s.X.(*ast.Ident)
	return ok && id.Name == x && s.Sel.Name == sel
}

func isRequireAdmin(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	return ok && isSelector(call.Fun, "h", "requireAdmin")
}

// permName 取权限码实参：空串字面量或 service.PermXxx。
func permName(t *testing.T, pattern string, e ast.Expr) string {
	t.Helper()
	if lit, ok := e.(*ast.BasicLit); ok && lit.Value == `""` {
		return ""
	}
	s, ok := e.(*ast.SelectorExpr)
	if ok && isSelector(s, "service", s.Sel.Name) && strings.HasPrefix(s.Sel.Name, "Perm") {
		return s.Sel.Name
	}
	t.Fatalf("%s: 权限码实参须是空串或 service.PermXxx", pattern)
	return ""
}

func metaOf(t *testing.T, pattern string, handler ast.Expr) routeMeta {
	t.Helper()
	if !isRequireAdmin(handler) {
		if pattern != adminLoginPath {
			t.Fatalf("%s 没有经过 requireAdmin", pattern)
		}
		return routeMeta{public: true}
	}
	call := handler.(*ast.CallExpr)
	if len(call.Args) < 2 {
		t.Fatalf("%s: requireAdmin 实参不足", pattern)
	}
	m := routeMeta{perm: permName(t, pattern, call.Args[0])}
	for _, opt := range call.Args[2:] {
		oc, ok := opt.(*ast.CallExpr)
		id, isIdent := func() (*ast.Ident, bool) {
			if !ok {
				return nil, false
			}
			i, ok := oc.Fun.(*ast.Ident)
			return i, ok
		}()
		if !isIdent {
			t.Fatalf("%s: 不认识的鉴权选项", pattern)
		}
		switch id.Name {
		case optFresh:
			m.fresh = true
		case optSetupExempt:
			m.setupExempt = true
		case optPerm:
			m.extraPerm = permName(t, pattern, oc.Args[0])
		default:
			t.Fatalf("%s: 不认识的鉴权选项 %s", pattern, id.Name)
		}
	}
	return m
}

// contractRoutes 读 hub-api.yaml：返回全部管理端接口与其中 403 引用 ReauthRequired 的接口。
func contractRoutes(t *testing.T) (all, reauth []string) {
	t.Helper()
	raw, err := os.ReadFile(contractAPI)
	if err != nil {
		t.Fatalf("read %s: %v", contractAPI, err)
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", contractAPI, err)
	}
	for path, ops := range doc.Paths {
		if !strings.HasPrefix(path, adminPrefix) {
			continue
		}
		for method, node := range ops {
			if !slices.Contains(httpMethods, method) {
				continue // 路径级的 parameters 等
			}
			var op struct {
				Responses map[string]struct {
					Ref string `yaml:"$ref"`
				} `yaml:"responses"`
			}
			if err := node.Decode(&op); err != nil {
				t.Fatalf("decode %s %s: %v", method, path, err)
			}
			key := strings.ToUpper(method) + " " + path
			all = append(all, key)
			if strings.HasSuffix(op.Responses["403"].Ref, reauthRefSuffix) {
				reauth = append(reauth, key)
			}
		}
	}
	slices.Sort(all)
	slices.Sort(reauth)
	return all, reauth
}

func sortedKeys(m map[string]routeMeta, keep func(routeMeta) bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if keep(v) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func TestAdminRoutePolicies(t *testing.T) {
	routes := parseAdminRoutes(t)
	all, reauth := contractRoutes(t)
	if got := slices.Sorted(maps.Keys(routes)); !slices.Equal(got, all) {
		t.Fatalf("router.go 的管理端路由与 hub-api.yaml 不一致\nrouter: %v\nyaml:   %v", got, all)
	}
	if got := sortedKeys(routes, func(m routeMeta) bool { return m.fresh }); !slices.Equal(got, sortedCopy(freshRoutes)) {
		t.Fatalf("挂了 withFresh 的路由与高危清单（docs/03 §4.4）不一致\ngot:  %v\nwant: %v", got, sortedCopy(freshRoutes))
	}
	if !slices.Equal(reauth, sortedCopy(freshRoutes)) {
		t.Fatalf("hub-api.yaml 里引用 ReauthRequired 的接口与高危清单不一致\nyaml: %v\nwant: %v", reauth, sortedCopy(freshRoutes))
	}
	if got := sortedKeys(routes, func(m routeMeta) bool { return m.setupExempt }); !slices.Equal(got, sortedCopy(setupExemptRoutes)) {
		t.Fatalf("R10 豁免清单不一致\ngot:  %v\nwant: %v", got, sortedCopy(setupExemptRoutes))
	}
	if got := sortedKeys(routes, func(m routeMeta) bool { return !m.public && m.perm == "" }); !slices.Equal(got, sortedCopy(loginOnlyRoutes)) {
		t.Fatalf("只需登录的路由不一致（其余管理端路由都必须挂权限码）\ngot:  %v\nwant: %v", got, sortedCopy(loginOnlyRoutes))
	}
	for pattern, m := range routes {
		if m.fresh && m.perm == "" {
			t.Fatalf("%s: 高危接口必须同时挂权限码", pattern)
		}
		// 超管专属码的写接口都是高危接口
		if m.perm == "PermAdminWrite" || m.perm == "PermHostWrite" || m.extraPerm == "PermHostWrite" {
			if !m.fresh {
				t.Fatalf("%s: admin:write / host:write 接口必须挂 withFresh", pattern)
			}
		}
	}
}

// concretePath 把 pattern 里的路径参数换成占位值，返回 method 与 path。
func concretePath(pattern string) (string, string) {
	method, path, _ := strings.Cut(pattern, " ")
	for strings.Contains(path, "{") {
		i, j := strings.Index(path, "{"), strings.Index(path, "}")
		path = path[:i] + "1" + path[j+1:]
	}
	return method, path
}

// 运行时核对：未登录一律 401；二次验证过期的超管调高危接口一律 reauth_required（不执行动作）；
// 没有权限码的管理员即使过期也只得到 permission_denied（先判权限码，R12）。
func TestAdminRoutePoliciesAtRuntime(t *testing.T) {
	srv, rbac := newAPIServer(t)
	root := bootstrapRoot(t, srv, rbac)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/admins",
		map[string]any{"username": "nobody1", "password": cred, "roles": []string{}}), http.StatusOK, codeOK)
	nobody := newClient(t, srv.URL)
	nobody.login("nobody1")
	anonymous := newClient(t, srv.URL)
	root.clock.advance(testReauthWindow + 1)

	routes := parseAdminRoutes(t)
	for pattern, m := range routes {
		if m.public {
			continue
		}
		method, path := concretePath(pattern)
		expectStatus(t, anonymous.do(method, path, nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))
		if !m.fresh {
			continue
		}
		t.Run(pattern, func(t *testing.T) {
			expectStatus(t, root.do(method, path, map[string]any{}), http.StatusForbidden, string(service.CodeReauthRequired))
			expectStatus(t, nobody.do(method, path, map[string]any{}), http.StatusForbidden, string(service.CodePermissionDenied))
		})
	}
	// 非高危的写接口不受二次验证影响（这里取套餐：参数不合法应走到 handler 返回 400）
	expectStatus(t, root.do(http.MethodPost, "/api/admin/plans", map[string]any{"extra": 1}), http.StatusBadRequest, string(service.CodeInvalidArgument))
}
