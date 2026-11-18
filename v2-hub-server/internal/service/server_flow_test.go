package service

import (
	"context"
	"testing"
)

// TestServerClientFlowReconciles：开关服务器的 VLESS 流控后，对账把面板上的 client 带上或去掉 flow（Ruling-77）。
func TestServerClientFlowReconciles(t *testing.T) {
	f := newUserFixture(t)
	ctx := context.Background()
	plan := f.plan(t, "basic", PlanQuota{TrafficLimit: gib, DurationDays: 30, LimitIP: 2})
	u := f.newUser(t, "flow.example", plan.ID)
	f.reconcile(t)
	email := f.emailOf(t, u.ID)
	if c := f.panelClient(t, email); c.Flow != "" {
		t.Fatalf("flow should be empty by default, got %q", c.Flow)
	}

	_, err := f.users.SetServerClientFlow(ctx, f.actor, f.server.ID, "xtls-rprx-direct", nil)
	wantCode(t, err, CodeInvalidArgument)
	_, err = f.users.SetServerClientFlow(ctx, f.actor, 999999, ClientFlowVision, nil)
	wantCode(t, err, CodeNotFound)

	srv, err := f.users.SetServerClientFlow(ctx, f.actor, f.server.ID, ClientFlowVision, nil)
	if err != nil || srv.ClientFlow != ClientFlowVision {
		t.Fatalf("SetServerClientFlow = %+v, %v", srv, err)
	}
	select {
	case <-f.users.ReconcileRequests():
	default:
		t.Fatal("turning the flow on must request a reconcile")
	}
	f.reconcile(t)
	if c := f.panelClient(t, email); c.Flow != ClientFlowVision || !c.Enabled || c.LimitIP != 2 {
		t.Fatalf("client after flow on = %+v", c)
	}

	if _, err := f.users.SetServerClientFlow(ctx, f.actor, f.server.ID, "", nil); err != nil {
		t.Fatalf("turn off: %v", err)
	}
	f.reconcile(t)
	if c := f.panelClient(t, email); c.Flow != "" {
		t.Fatalf("flow should be removed, got %q", c.Flow)
	}

	logs, err := f.rbac.ListAuditLogs(ctx, PageQuery{PageSize: 50})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	var audited int
	for _, l := range logs.Items {
		if l.Action == ActionServerClientFlow {
			audited++
		}
	}
	if audited != 2 {
		t.Fatalf("want two flow audits, got %d", audited)
	}
}
