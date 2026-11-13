package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"v2-hub-server/internal/service"
)

// 管理员发公告、开签到 → 用户注册 → 仪表盘看到公告 → 提工单 → 管理员回复 → 用户看到回复，全程走 HTTP。
func TestContentTicketEndpoints(t *testing.T) {
	srv, rbac := newAPIServer(t)
	if _, err := rbac.BootstrapSuperAdmin(context.Background(), "root", cred); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	root := newClient(t, srv.URL)
	root.login("root")
	expectStatus(t, root.do(http.MethodPut, "/api/admin/settings", map[string]any{
		"register": map[string]any{"mode": service.RegisterOpen}, "checkin": map[string]any{"enabled": true},
	}), http.StatusOK, codeOK)

	ann := root.do(http.MethodPost, "/api/admin/announcements",
		map[string]any{"title": "上线公告", "content": "# 欢迎", "isPinned": false, "isPopup": true, "isPublished": true})
	expectStatus(t, ann, http.StatusOK, codeOK)
	var created announcementVO
	if err := json.Unmarshal(ann.Data, &created); err != nil || created.PublishedAt == nil || created.CreatedAt == nil {
		t.Fatalf("created announcement = %+v, %v", created, err)
	}
	// 邮件未配置：群发不可用
	expectStatus(t, root.do(http.MethodPost, fmt.Sprintf("/api/admin/announcements/%d/mail", created.ID), nil),
		http.StatusForbidden, string(service.CodeFeatureDisabled))
	expectStatus(t, root.do(http.MethodPost, "/api/admin/tutorials", map[string]any{
		"platform": "ios", "clientName": "Shadowrocket", "isPublished": true,
		"links": []map[string]any{{"label": "App Store", "url": "javascript:alert(1)"}},
	}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	expectStatus(t, root.do(http.MethodPost, "/api/admin/tutorials", map[string]any{
		"platform": "ios", "clientName": "Shadowrocket", "isPublished": true,
		"links": []map[string]any{{"label": "App Store", "url": "https://apps.apple.com/app/id932747118"}},
	}), http.StatusOK, codeOK)

	alice := newClient(t, srv.URL)
	reg := alice.do(http.MethodPost, "/api/user/auth/register", map[string]any{"username": "alice", "email": "alice@example.com", "password": cred})
	expectStatus(t, reg, http.StatusOK, codeOK)
	var session userSessionVO
	if err := json.Unmarshal(reg.Data, &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	alice.csrf = session.CSRFToken

	dash := alice.do(http.MethodGet, "/api/user/dashboard", nil)
	expectStatus(t, dash, http.StatusOK, codeOK)
	var d dashboardVO
	if err := json.Unmarshal(dash.Data, &d); err != nil || d.Announcement == nil || d.PopupAnnouncement == nil || d.PopupAnnouncement.ID != created.ID {
		t.Fatalf("dashboard announcements = %+v, %v", d, err)
	}
	// 用户侧的公告不带管理端字段
	var rawDash map[string]map[string]any
	if err := json.Unmarshal(dash.Data, &rawDash); err == nil {
		if _, leaked := rawDash["announcement"]["createdAt"]; leaked {
			t.Fatal("user-facing announcement must not expose admin-only fields")
		}
	}
	tut := alice.do(http.MethodGet, "/api/user/tutorials", nil)
	expectStatus(t, tut, http.StatusOK, codeOK)
	var tutorials []tutorialVO
	if err := json.Unmarshal(tut.Data, &tutorials); err != nil || len(tutorials) != 1 || len(tutorials[0].Links) != 1 {
		t.Fatalf("tutorials = %+v, %v", tutorials, err)
	}
	expectStatus(t, alice.do(http.MethodGet, "/api/user/shared-accounts", nil), http.StatusForbidden, string(service.CodeFeatureDisabled))

	// 没有套餐：签到 409
	ci := alice.do(http.MethodGet, "/api/user/checkin", nil)
	expectStatus(t, ci, http.StatusOK, codeOK)
	var info checkinInfoVO
	if err := json.Unmarshal(ci.Data, &info); err != nil || !info.Enabled || info.CanCheckin || info.Reason == "" || info.Logs == nil {
		t.Fatalf("checkin info = %+v, %v", info, err)
	}
	expectStatus(t, alice.do(http.MethodPost, "/api/user/checkin", nil), http.StatusConflict, string(service.CodeConflict))

	// 工单：新建 → 管理员回复 → 用户看到回复且看不到管理员用户名
	tk := alice.do(http.MethodPost, "/api/user/tickets", map[string]any{"subject": "连不上", "content": "香港节点超时"})
	expectStatus(t, tk, http.StatusOK, codeOK)
	var ticket ticketDetailVO
	if err := json.Unmarshal(tk.Data, &ticket); err != nil || ticket.Status != service.TicketOpen || ticket.UserID != nil {
		t.Fatalf("created ticket = %+v, %v", ticket, err)
	}
	path := fmt.Sprintf("/api/admin/tickets/%d", ticket.ID)
	expectStatus(t, root.do(http.MethodGet, "/api/admin/tickets?status=open", nil), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPost, path+"/messages", map[string]any{"content": ""}), http.StatusBadRequest, string(service.CodeInvalidArgument))
	replied := root.do(http.MethodPost, path+"/messages", map[string]any{"content": "已处理，请重试"})
	expectStatus(t, replied, http.StatusOK, codeOK)
	var adminView ticketDetailVO
	if err := json.Unmarshal(replied.Data, &adminView); err != nil || adminView.Status != service.TicketAnswered || adminView.Username != "alice" || adminView.Messages[1].AuthorName != "root" {
		t.Fatalf("admin view = %+v, %v", adminView, err)
	}
	own := alice.do(http.MethodGet, fmt.Sprintf("/api/user/tickets/%d", ticket.ID), nil)
	expectStatus(t, own, http.StatusOK, codeOK)
	var userView ticketDetailVO
	if err := json.Unmarshal(own.Data, &userView); err != nil || len(userView.Messages) != 2 || userView.Messages[1].AuthorName != "" || userView.Messages[1].Content != "已处理，请重试" {
		t.Fatalf("user view = %+v, %v", userView, err)
	}

	// 另一个用户读不到 alice 的工单（R8）
	bob := newClient(t, srv.URL)
	breg := bob.do(http.MethodPost, "/api/user/auth/register", map[string]any{"username": "bob", "email": "bob@example.com", "password": cred})
	expectStatus(t, breg, http.StatusOK, codeOK)
	if err := json.Unmarshal(breg.Data, &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	bob.csrf = session.CSRFToken
	expectStatus(t, bob.do(http.MethodGet, fmt.Sprintf("/api/user/tickets/%d", ticket.ID), nil), http.StatusNotFound, string(service.CodeNotFound))
	expectStatus(t, bob.do(http.MethodPost, fmt.Sprintf("/api/user/tickets/%d/close", ticket.ID), nil), http.StatusNotFound, string(service.CodeNotFound))

	// 只有 node:read 的管理员碰不到内容与工单（R1）
	expectStatus(t, root.do(http.MethodPost, "/api/admin/roles",
		map[string]any{"code": "viewer", "name": "只读", "permissions": []string{service.PermNodeRead}}), http.StatusOK, codeOK)
	expectStatus(t, root.do(http.MethodPost, "/api/admin/admins",
		map[string]any{"username": "viewer1", "password": cred, "roles": []string{"viewer"}}), http.StatusOK, codeOK)
	viewer := newClient(t, srv.URL)
	viewer.login("viewer1")
	for _, p := range []string{"/api/admin/announcements", "/api/admin/tutorials", "/api/admin/shared-accounts", "/api/admin/tickets", path} {
		expectStatus(t, viewer.do(http.MethodGet, p, nil), http.StatusForbidden, string(service.CodePermissionDenied))
	}
	expectStatus(t, viewer.do(http.MethodPost, path+"/messages", map[string]any{"content": "x"}), http.StatusForbidden, string(service.CodePermissionDenied))
}
