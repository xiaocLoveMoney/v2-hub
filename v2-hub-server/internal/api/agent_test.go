package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/service"
)

// 二级代理的越权矩阵（docs/03 §5.23，Ruling-101）。路由清单一律取自 router.go 源码（parseAdminRoutes），
// 新增管理端路由或代理接口会自动进入矩阵；代理接口出现新的资源类型、或新的按 id 写接口需要请求体而这里没登记时，用例直接失败提示补齐。

const (
	agentGiB      = int64(1) << 30
	agentUserPass = "password-123"
	missingID     = int64(987654321)
)

type agentWorld struct {
	t      *testing.T
	pool   *pgxpool.Pool
	root   *apiClient
	nobody *apiClient
	agentA *apiClient
	agentB *apiClient
	aID    int64
	bID    int64
	planA  int64
	planB  int64
	// A 名下的对象
	userA, ticketA, inviteA int64
	// 不在 A 名下的对象：B 名下、平台的、A 名下但待删除的
	userB, userPlat, userGoneA       int64
	ticketB, ticketPlat, ticketGoneA int64
	inviteB, inviteAdmin, invitePers int64
}

func dataID(t *testing.T, res apiResult) int64 {
	t.Helper()
	var v struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(res.Data, &v); err != nil || v.ID == 0 {
		t.Fatalf("decode id from %s: %v", res.Data, err)
	}
	return v.ID
}

func (w *agentWorld) queryID(sql string, args ...any) int64 {
	w.t.Helper()
	var id int64
	if err := w.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		w.t.Fatalf("query %q: %v", sql, err)
	}
	return id
}

func (w *agentWorld) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.pool.Exec(context.Background(), sql, args...); err != nil {
		w.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (w *agentWorld) ok(c *apiClient, method, path string, body any) apiResult {
	w.t.Helper()
	res := c.do(method, path, body)
	expectStatus(w.t, res, http.StatusOK, codeOK)
	return res
}

func (w *agentWorld) createAgent(name string, planID int64) int64 {
	w.t.Helper()
	return dataID(w.t, w.ok(w.root, http.MethodPost, "/api/admin/agents", map[string]any{
		"username": name, "password": cred, "remark": "", "maxUsers": 10, "trafficPoolTotal": 100 * agentGiB, "planIds": []int64{planID},
	}))
}

func (w *agentWorld) agentUser(c *apiClient, name string, planID int64) int64 {
	w.t.Helper()
	return dataID(w.t, w.ok(c, http.MethodPost, "/api/admin/agent/users", map[string]any{"username": name, "password": agentUserPass, "planId": planID}))
}

func (w *agentWorld) ticket(userID int64, subject string) int64 {
	w.t.Helper()
	id := w.queryID(`INSERT INTO ticket (user_id, subject) VALUES ($1, $2) RETURNING id`, userID, subject)
	w.exec(`INSERT INTO ticket_message (ticket_id, author_kind, content) VALUES ($1, 'user', 'help')`, id)
	return id
}

func (w *agentWorld) order(userID int64, no string) {
	w.t.Helper()
	w.exec(`INSERT INTO shop_order (order_no, user_id, username, kind, item, amount_original, amount_paid)
		SELECT $2, id, username, 'plan', '{"name":"x"}'::jsonb, 100, 100 FROM end_user WHERE id = $1`, userID, no)
}

func (w *agentWorld) ledger(userID int64) {
	w.t.Helper()
	w.exec(`INSERT INTO wallet_ledger (user_id, username, kind, amount, balance_after, admin_id)
		SELECT u.id, u.username, 'admin_adjust', 100, 100, (SELECT id FROM admin WHERE username = 'root') FROM end_user u WHERE u.id = $1`, userID)
}

// newAgentWorld：两个代理 A、B（白名单各一个套餐），A、B 各有名下用户，另有平台用户与 A 名下的待删除用户；
// 每个用户各一张工单、一笔订单、一条员工调整的余额流水；A、B 各一个邀请码，另有管理员码与平台用户的个人码。
func newAgentWorld(t *testing.T) *agentWorld {
	t.Helper()
	srv, rbac, pool := newAPIServerWithPool(t)
	w := &agentWorld{t: t, pool: pool}
	w.root = bootstrapRoot(t, srv, rbac)
	w.planA = w.queryID(`INSERT INTO plan (name, traffic_limit, duration_days) VALUES ('pa', $1, 30) RETURNING id`, agentGiB)
	w.planB = w.queryID(`INSERT INTO plan (name, traffic_limit, duration_days) VALUES ('pb', $1, 30) RETURNING id`, agentGiB)
	w.aID = w.createAgent("agent-a", w.planA)
	w.bID = w.createAgent("agent-b", w.planB)
	w.ok(w.root, http.MethodPost, "/api/admin/admins", map[string]any{"username": "nobody1", "password": cred, "roles": []string{}})
	w.nobody = newClient(t, srv.URL)
	w.nobody.login("nobody1")
	w.agentA = newClient(t, srv.URL)
	w.agentA.login("agent-a")
	w.agentB = newClient(t, srv.URL)
	w.agentB.login("agent-b")

	w.userA = w.agentUser(w.agentA, "ua1", w.planA)
	w.userGoneA = w.agentUser(w.agentA, "ua-gone", w.planA)
	w.userB = w.agentUser(w.agentB, "ub1", w.planB)
	w.userPlat = dataID(t, w.ok(w.root, http.MethodPost, "/api/admin/users", map[string]any{"username": "plat1", "password": agentUserPass}))
	for _, id := range []int64{w.userA, w.userGoneA, w.userB, w.userPlat} {
		w.order(id, fmt.Sprintf("O%d", id))
		w.ledger(id)
	}
	w.ticketA, w.ticketGoneA = w.ticket(w.userA, "a"), w.ticket(w.userGoneA, "gone")
	w.ticketB, w.ticketPlat = w.ticket(w.userB, "b"), w.ticket(w.userPlat, "plat")
	// 待删除：A 名下但已标记删除（等面板清理时的状态），归属列仍是 A
	w.exec(`UPDATE end_user SET deleted_at = now() WHERE id = $1`, w.userGoneA)
	// 让“清除两步验证”在越权时能看出变化
	w.exec(`UPDATE end_user SET totp_secret_enc = '\x01'::bytea WHERE id = ANY($1)`, []int64{w.userB, w.userPlat, w.userGoneA})

	code := map[string]any{"maxUses": 0, "isDisabled": false}
	w.inviteA = dataID(t, w.ok(w.agentA, http.MethodPost, "/api/admin/agent/invite-codes", code))
	w.inviteB = dataID(t, w.ok(w.agentB, http.MethodPost, "/api/admin/agent/invite-codes", code))
	w.inviteAdmin = dataID(t, w.ok(w.root, http.MethodPost, "/api/admin/invite-codes", code))
	w.invitePers = w.queryID(`INSERT INTO invite_code (code, owner_user_id) VALUES ('ABCDEFGHJK', $1) RETURNING id`, w.userPlat)
	return w
}

// snapshot 把越权可能改动的全部数据拼成一个字符串：用户（密码哈希、配额、订阅凭据版本、两步验证、归属、删除标记）、
// 工单与消息数、邀请码、代理配额与开通记录、用户会话。矩阵前后必须完全相同。
func (w *agentWorld) snapshot() string {
	w.t.Helper()
	const q = `SELECT
	  (SELECT string_agg(concat_ws('|', id, username, password_hash, remark, plan_id, traffic_limit, traffic_used, expires_at, pending_days,
	      is_disabled, status, cred_version, encode(sub_token_hash, 'hex'), md5(client_cred_enc), totp_secret_enc IS NOT NULL, deleted_at, agent_id, balance), ';' ORDER BY id) FROM end_user),
	  (SELECT string_agg(concat_ws('|', t.id, t.status, t.last_reply_at, t.closed_at, (SELECT count(*) FROM ticket_message m WHERE m.ticket_id = t.id)), ';' ORDER BY t.id) FROM ticket t),
	  (SELECT string_agg(concat_ws('|', id, code, max_uses, used_count, expires_at, is_disabled, remark, agent_id, owner_user_id), ';' ORDER BY id) FROM invite_code),
	  (SELECT string_agg(concat_ws('|', admin_id, max_users, traffic_pool_total, traffic_pool_used), ';' ORDER BY admin_id) FROM agent_profile),
	  (SELECT count(*) FROM agent_allocation), (SELECT count(*) FROM user_session)`
	var users, tickets, invites, profiles string
	var allocations, sessions int64
	if err := w.pool.QueryRow(context.Background(), q).Scan(&users, &tickets, &invites, &profiles, &allocations, &sessions); err != nil {
		w.t.Fatalf("snapshot: %v", err)
	}
	return strings.Join([]string{users, tickets, invites, profiles, fmt.Sprint(allocations), fmt.Sprint(sessions)}, "\n")
}

// agentRouteBodies 是代理侧按 id 写接口的合法请求体：越权时若不是 404 而是 400，说明归属校验排在参数校验之后或请求体不对，用例都会失败。
func (w *agentWorld) agentRouteBodies() map[string]any {
	return map[string]any{
		"PUT /api/admin/agent/users/{id}":             map[string]any{"remark": "pwned", "isDisabled": true},
		"POST /api/admin/agent/users/{id}/plan":       map[string]any{"planId": w.planA},
		"PUT /api/admin/agent/users/{id}/quota":       map[string]any{"trafficLimit": 1},
		"PUT /api/admin/agent/users/{id}/password":    map[string]any{"password": "pwned-password-1"},
		"PUT /api/admin/agent/invite-codes/{id}":      map[string]any{"maxUses": 1, "isDisabled": true, "remark": "pwned"},
		"POST /api/admin/agent/tickets/{id}/messages": map[string]any{"content": "pwned"},
	}
}

// resourceOf 返回代理接口路径里的资源类型（/api/admin/agent/<资源>/...）。
func resourceOf(t *testing.T, pattern string) string {
	t.Helper()
	_, path, _ := strings.Cut(pattern, " ")
	rest := strings.TrimPrefix(path, agentPrefix)
	res, _, _ := strings.Cut(rest, "/")
	return res
}

func withID(pattern string, id int64) (string, string) {
	method, path, _ := strings.Cut(pattern, " ")
	return method, strings.Replace(path, "{id}", fmt.Sprint(id), 1)
}

// 矩阵 1：代理会话访问全部员工路由一律 403，只有 withSelf 的本人接口例外；矩阵 2：员工（含超管、无权限的管理员）访问全部代理接口一律 403。
func TestAgentIsolationRouteMatrix(t *testing.T) {
	w := newAgentWorld(t)
	routes := parseAdminRoutes(t)
	staffRoutes, agentRoutes := 0, 0
	for pattern, m := range routes {
		method, path := concretePath(pattern)
		switch {
		case m.public || m.self:
			continue
		case m.agent:
			agentRoutes++
			expectStatus(t, w.root.do(method, path, map[string]any{}), http.StatusForbidden, string(service.CodePermissionDenied))
			expectStatus(t, w.nobody.do(method, path, map[string]any{}), http.StatusForbidden, string(service.CodePermissionDenied))
		default:
			staffRoutes++
			res := w.agentA.do(method, path, map[string]any{})
			if res.Status != http.StatusForbidden || res.Body.Code != string(service.CodePermissionDenied) {
				t.Errorf("agent → %s: got %d %q, want 403 permission_denied", pattern, res.Status, res.Body.Code)
			}
		}
	}
	if staffRoutes < 100 || agentRoutes < 20 {
		t.Fatalf("route matrix too small: staff=%d agent=%d", staffRoutes, agentRoutes)
	}
	// 本人接口对代理照常可用；会话信息标明账号类型且没有任何权限
	s := w.agentA.me()
	if s.Kind != service.AdminKindAgent || len(s.Permissions) != 0 || s.IsSuperAdmin || s.IsOwner || s.TOTPSetupRequired || len(s.MenuOrder) != 0 {
		t.Fatalf("agent session = %+v", s)
	}
	if root := w.root.me(); root.Kind != service.AdminKindStaff {
		t.Fatalf("root session kind = %q", root.Kind)
	}
	// 代理接口同样要求 CSRF
	noCSRF := *w.agentA
	noCSRF.csrf = ""
	expectStatus(t, noCSRF.do(http.MethodPost, "/api/admin/agent/invite-codes", map[string]any{"maxUses": 0, "isDisabled": false}),
		http.StatusForbidden, string(service.CodePermissionDenied))
}

// 矩阵 3：代理 A 对每一个按 id 的代理接口，传 B 名下、平台的、A 名下但待删除的、不存在的对象 id → 一律 404，库里数据没有任何变化。
// 之后用 A 自己的对象再调一遍，确认这些请求在归属正确时能成功（排除 404 来自别的原因）。
func TestAgentIsolationForeignIDs(t *testing.T) {
	w := newAgentWorld(t)
	foreign := map[string][]int64{
		"users":        {w.userB, w.userPlat, w.userGoneA, missingID},
		"tickets":      {w.ticketB, w.ticketPlat, w.ticketGoneA, missingID},
		"invite-codes": {w.inviteB, w.inviteAdmin, w.invitePers, missingID},
	}
	own := map[string]int64{"users": w.userA, "tickets": w.ticketA, "invite-codes": w.inviteA}
	bodies := w.agentRouteBodies()
	routes := parseAdminRoutes(t)
	var idRoutes []string
	for pattern, m := range routes {
		if m.agent && strings.Contains(pattern, "{id}") {
			idRoutes = append(idRoutes, pattern)
		}
	}
	slices.Sort(idRoutes)
	before := w.snapshot()
	checked := 0
	for _, pattern := range idRoutes {
		res := resourceOf(t, pattern)
		ids, known := foreign[res]
		if !known {
			t.Fatalf("%s: 代理接口出现了新的资源类型 %q，请在越权矩阵里补上它的对象", pattern, res)
		}
		method, _, _ := strings.Cut(pattern, " ")
		body, hasBody := bodies[pattern]
		if !hasBody && (method == http.MethodPut || strings.HasSuffix(pattern, "/plan") || strings.HasSuffix(pattern, "/messages")) {
			t.Fatalf("%s: 按 id 的写接口需要在 agentRouteBodies 里登记合法请求体", pattern)
		}
		for _, id := range ids {
			m, path := withID(pattern, id)
			got := w.agentA.do(m, path, body)
			if got.Status != http.StatusNotFound || got.Body.Code != string(service.CodeNotFound) {
				t.Errorf("agent A %s (id %d): got %d %q, want 404 not_found", pattern, id, got.Status, got.Body.Code)
			}
			checked++
		}
	}
	if after := w.snapshot(); after != before {
		t.Fatalf("越权请求改动了数据\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if checked < len(idRoutes)*4 || len(idRoutes) < 10 {
		t.Fatalf("id matrix too small: routes=%d checked=%d", len(idRoutes), checked)
	}
	// 正向对照：同样的请求用 A 自己的对象都能成功；删除类放最后，先清两步验证、删邀请码，再删用户
	var deletes []string
	for _, pattern := range idRoutes {
		if strings.HasPrefix(pattern, http.MethodDelete+" ") {
			deletes = append(deletes, pattern)
			continue
		}
		m, path := withID(pattern, own[resourceOf(t, pattern)])
		if got := w.agentA.do(m, path, bodies[pattern]); got.Status != http.StatusOK {
			t.Errorf("agent A own %s: got %d %q (%s)", pattern, got.Status, got.Body.Code, got.Body.Message)
		}
	}
	slices.SortFunc(deletes, func(a, b string) int { return len(b) - len(a) })
	for _, pattern := range deletes {
		m, path := withID(pattern, own[resourceOf(t, pattern)])
		if got := w.agentA.do(m, path, nil); got.Status != http.StatusOK {
			t.Errorf("agent A own %s: got %d %q (%s)", pattern, got.Status, got.Body.Code, got.Body.Message)
		}
	}
}

// 矩阵 4：代理 A 的各个列表接口只出现名下数据；传不在名下的 userId 返回空。代理侧看不到员工账号名。
func TestAgentIsolationLists(t *testing.T) {
	w := newAgentWorld(t)
	// 员工回复 A 名下用户的工单：A 看到的 authorName 为空；A 自己回复的带他的用户名
	w.ok(w.root, http.MethodPost, fmt.Sprintf("/api/admin/tickets/%d/messages", w.ticketA), map[string]any{"content": "staff reply"})
	w.ok(w.agentA, http.MethodPost, fmt.Sprintf("/api/admin/agent/tickets/%d/messages", w.ticketA), map[string]any{"content": "agent reply"})
	var detail ticketDetailVO
	if err := json.Unmarshal(w.ok(w.agentA, http.MethodGet, fmt.Sprintf("/api/admin/agent/tickets/%d", w.ticketA), nil).Data, &detail); err != nil {
		t.Fatalf("decode ticket: %v", err)
	}
	var names []string
	for _, m := range detail.Messages {
		names = append(names, m.AuthorKind+":"+m.AuthorName)
	}
	if !slices.Equal(names, []string{"user:", "admin:", "admin:agent-a"}) {
		t.Fatalf("message authors seen by agent = %v", names)
	}

	ownUsers := []int64{w.userA}
	type listItem struct {
		ID            int64   `json:"id"`
		UserID        *int64  `json:"userId"`
		AgentID       *int64  `json:"agentId"`
		Kind          string  `json:"kind"`
		AdminUsername *string `json:"adminUsername"`
	}
	decode := func(res apiResult) pageVO[listItem] {
		var page pageVO[listItem]
		if err := json.Unmarshal(res.Data, &page); err != nil {
			t.Fatalf("decode page: %v", err)
		}
		return page
	}
	userScoped := func(path string) {
		page := decode(w.ok(w.agentA, http.MethodGet, path, nil))
		if page.Total == 0 {
			t.Fatalf("%s: expected own rows", path)
		}
		for _, it := range page.Items {
			if it.UserID == nil || !slices.Contains(ownUsers, *it.UserID) {
				t.Fatalf("%s: foreign row %+v", path, it)
			}
			if it.AdminUsername != nil && *it.AdminUsername != "" {
				t.Fatalf("%s: staff username leaked: %+v", path, it)
			}
		}
		for _, foreignUser := range []int64{w.userB, w.userPlat, w.userGoneA} {
			if p := decode(w.ok(w.agentA, http.MethodGet, fmt.Sprintf("%s?userId=%d", path, foreignUser), nil)); p.Total != 0 || len(p.Items) != 0 {
				t.Fatalf("%s?userId=%d: got %+v", path, foreignUser, p)
			}
		}
	}
	checks := map[string]func(path string){
		"GET /api/admin/agent/summary": func(path string) {
			var s agentSummaryVO
			if err := json.Unmarshal(w.ok(w.agentA, http.MethodGet, path, nil).Data, &s); err != nil || s.UserCount != 1 || s.MaxUsers != 10 {
				t.Fatalf("summary = %+v, %v", s, err)
			}
		},
		"GET /api/admin/agent/plans": func(path string) {
			var plans []agentPlanVO
			if err := json.Unmarshal(w.ok(w.agentA, http.MethodGet, path, nil).Data, &plans); err != nil || len(plans) != 1 || plans[0].ID != w.planA || plans[0].PoolCost != agentGiB {
				t.Fatalf("plans = %+v, %v", plans, err)
			}
		},
		"GET /api/admin/agent/users": func(path string) {
			page := decode(w.ok(w.agentA, http.MethodGet, path, nil))
			if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != w.userA {
				t.Fatalf("users = %+v", page)
			}
			if p := decode(w.ok(w.agentA, http.MethodGet, path+"?keyword=ub1", nil)); p.Total != 0 {
				t.Fatalf("keyword matched foreign user: %+v", p)
			}
		},
		"GET /api/admin/agent/tickets":       userScoped,
		"GET /api/admin/agent/orders":        userScoped,
		"GET /api/admin/agent/wallet-ledger": userScoped,
		"GET /api/admin/agent/invite-codes": func(path string) {
			page := decode(w.ok(w.agentA, http.MethodGet, path, nil))
			if page.Total != 1 || page.Items[0].ID != w.inviteA || page.Items[0].Kind != service.InviteKindAgent || page.Items[0].AgentID == nil || *page.Items[0].AgentID != w.aID {
				t.Fatalf("invite codes = %+v", page)
			}
		},
		"GET /api/admin/agent/allocations": func(path string) {
			page := decode(w.ok(w.agentA, http.MethodGet, path, nil))
			if page.Total != 2 {
				t.Fatalf("allocations total = %d, want 2", page.Total)
			}
			for _, it := range page.Items {
				if it.AgentID == nil || *it.AgentID != w.aID {
					t.Fatalf("foreign allocation %+v", it)
				}
			}
		},
	}
	for pattern, m := range parseAdminRoutes(t) {
		if !m.agent || !strings.HasPrefix(pattern, http.MethodGet+" ") || strings.Contains(pattern, "{") {
			continue
		}
		check, ok := checks[pattern]
		if !ok {
			t.Fatalf("%s: 新的代理列表接口需要在越权矩阵里补上归属检查", pattern)
		}
		_, path, _ := strings.Cut(pattern, " ")
		check(path)
	}
}

// 未登录访问代理接口 401；代理被停用后会话立即失效；员工侧能看到并按代理筛选归属，管理员列表与管理员接口不含代理。
func TestAgentSessionAndStaffViews(t *testing.T) {
	w := newAgentWorld(t)
	anonymous := newClient(t, w.root.base)
	expectStatus(t, anonymous.do(http.MethodGet, "/api/admin/agent/summary", nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))

	// 员工的用户列表：带 agent、可按 agentId 筛选
	var users pageVO[userVO]
	if err := json.Unmarshal(w.ok(w.root, http.MethodGet, fmt.Sprintf("/api/admin/users?agentId=%d", w.bID), nil).Data, &users); err != nil {
		t.Fatalf("decode users: %v", err)
	}
	if users.Total != 1 || users.Items[0].ID != w.userB || users.Items[0].Agent == nil || users.Items[0].Agent.Username != "agent-b" {
		t.Fatalf("users of agent B = %+v", users)
	}
	var codes pageVO[inviteCodeVO]
	if err := json.Unmarshal(w.ok(w.root, http.MethodGet, fmt.Sprintf("/api/admin/invite-codes?kind=agent&agentId=%d", w.aID), nil).Data, &codes); err != nil {
		t.Fatalf("decode codes: %v", err)
	}
	if codes.Total != 1 || codes.Items[0].ID != w.inviteA || codes.Items[0].AgentUsername != "agent-a" {
		t.Fatalf("codes of agent A = %+v", codes)
	}
	// 管理员列表不含代理；PUT /admins/{id} 对代理 409（代理不能持有角色）
	var admins pageVO[adminVO]
	if err := json.Unmarshal(w.ok(w.root, http.MethodGet, "/api/admin/admins", nil).Data, &admins); err != nil {
		t.Fatalf("decode admins: %v", err)
	}
	for _, a := range admins.Items {
		if a.ID == w.aID || a.ID == w.bID {
			t.Fatalf("admin list contains agent %+v", a)
		}
	}
	expectStatus(t, w.root.do(http.MethodPut, fmt.Sprintf("/api/admin/admins/%d", w.aID), map[string]any{"roles": []string{"operator"}, "isDisabled": false}),
		http.StatusConflict, string(service.CodeConflict))
	// 超管划转：B 的用户划给 A，再收回
	w.ok(w.root, http.MethodPut, fmt.Sprintf("/api/admin/users/%d/agent", w.userB), map[string]any{"agentId": w.aID})
	w.ok(w.agentA, http.MethodGet, fmt.Sprintf("/api/admin/agent/users/%d", w.userB), nil)
	expectStatus(t, w.agentB.do(http.MethodGet, fmt.Sprintf("/api/admin/agent/users/%d", w.userB), nil), http.StatusNotFound, string(service.CodeNotFound))
	w.ok(w.root, http.MethodPut, fmt.Sprintf("/api/admin/users/%d/agent", w.userB), map[string]any{"agentId": nil})
	expectStatus(t, w.agentA.do(http.MethodGet, fmt.Sprintf("/api/admin/agent/users/%d", w.userB), nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, w.root.do(http.MethodPut, fmt.Sprintf("/api/admin/users/%d/agent", w.userB), map[string]any{}), http.StatusBadRequest, string(service.CodeInvalidArgument))

	// 停用代理 A：其会话立即失效
	w.ok(w.root, http.MethodPut, fmt.Sprintf("/api/admin/agents/%d", w.aID), map[string]any{
		"remark": "", "maxUsers": 10, "trafficPoolTotal": 100 * agentGiB, "planIds": []int64{w.planA}, "isDisabled": true,
	})
	expectStatus(t, w.agentA.do(http.MethodGet, "/api/admin/agent/summary", nil), http.StatusUnauthorized, string(service.CodeUnauthenticated))
	w.ok(w.agentB, http.MethodGet, "/api/admin/agent/summary", nil)
}
