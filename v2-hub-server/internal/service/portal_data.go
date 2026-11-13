package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 客户后台数据（docs/03 §5.5）。全部以会话里的用户为准，不接受外部传入的用户 id（R8）。

const (
	defaultTrafficHours = 72
	maxTrafficHours     = 720
	snapshotKeyCPU      = "cpu"
	msgAccountNotActive = "账号当前不可用，无法查看节点链接"
)

// Dashboard 是客户后台仪表盘数据。
type Dashboard struct {
	Status        string
	PlanName      string
	TrafficLimit  int64
	TrafficUsed   int64
	TrafficToday  int64
	ExpiresAt     *time.Time
	PendingDays   int
	LimitIP       int
	OnlineDevices int
	LastUsedAt    *time.Time
	NodeCount     int
	// Balance 是余额（分）。
	Balance int64
}

// GetDashboard 返回本人的套餐、用量、到期、在线设备与可用节点数。
func (s *UserService) GetDashboard(ctx context.Context, p *UserPrincipal) (*Dashboard, error) {
	now := s.now().In(s.cfg.Location)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.cfg.Location)
	row, err := s.q.GetUserDashboard(ctx, sqlcgen.GetUserDashboardParams{ID: p.UserID, DayStart: pgtype.Timestamptz{Time: dayStart, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newError(CodeNotFound, msgUserNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get dashboard of user %d: %w", p.UserID, err)
	}
	nodes, err := s.q.ListUserSubNodes(ctx, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("list sub nodes of user %d: %w", p.UserID, err)
	}
	return &Dashboard{
		Status: row.Status, PlanName: row.PlanName, TrafficLimit: row.TrafficLimit, TrafficUsed: row.TrafficUsed,
		TrafficToday: row.TrafficToday, ExpiresAt: timePtr(row.ExpiresAt), PendingDays: int(row.PendingDays), LimitIP: int(row.LimitIp),
		OnlineDevices: int(row.OnlineDevices), LastUsedAt: timePtr(row.LastUsedAt), NodeCount: len(nodes), Balance: row.Balance,
	}, nil
}

// TrafficPoint 是一个小时的用量。
type TrafficPoint struct {
	Hour time.Time
	Up   int64
	Down int64
}

// ListTraffic 返回本人最近 hours 小时按小时的用量（升序）；hours 为 0 取默认 72。
func (s *UserService) ListTraffic(ctx context.Context, p *UserPrincipal, hours int) ([]TrafficPoint, error) {
	if hours == 0 {
		hours = defaultTrafficHours
	}
	if hours < 1 || hours > maxTrafficHours {
		return nil, newError(CodeInvalidArgument, fmt.Sprintf("hours 取 1–%d", maxTrafficHours))
	}
	since := hourOf(s.now()).Add(-time.Duration(hours-1) * time.Hour)
	rows, err := s.q.ListUserTrafficHourly(ctx, sqlcgen.ListUserTrafficHourlyParams{UserID: p.UserID, Hour: pgtype.Timestamptz{Time: since, Valid: true}})
	if err != nil {
		return nil, fmt.Errorf("list traffic of user %d: %w", p.UserID, err)
	}
	out := make([]TrafficPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, TrafficPoint{Hour: r.Hour.Time, Up: r.Up, Down: r.Down})
	}
	return out, nil
}

// PortalNode 是客户后台看到的一个节点；ServerLoad 为 nil 表示未知。
type PortalNode struct {
	ID             int64
	Name           string
	Region         string
	Protocol       string
	IsServerOnline bool
	ServerLoad     *float64
	OnlineClients  int
}

// PortalNodeGroup 是一个节点组及其节点。
type PortalNodeGroup struct {
	ID    int64
	Name  string
	Nodes []PortalNode
}

func snapshotCPU(raw []byte) *float64 {
	if len(raw) == 0 {
		return nil
	}
	var snap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil
	}
	var cpu float64
	if err := json.Unmarshal(snap[snapshotKeyCPU], &cpu); err != nil {
		return nil
	}
	return &cpu
}

// ListPortalNodes 返回本人被授权且启用的节点，按节点组分段；状态取平台缓存，不回源面板。
func (s *UserService) ListPortalNodes(ctx context.Context, p *UserPrincipal) ([]PortalNodeGroup, error) {
	rows, err := s.q.ListUserNodeGroups(ctx, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("list node groups of user %d: %w", p.UserID, err)
	}
	out := make([]PortalNodeGroup, 0, 4)
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].ID != r.GroupID {
			out = append(out, PortalNodeGroup{ID: r.GroupID, Name: r.GroupName, Nodes: make([]PortalNode, 0, 8)})
		}
		g := &out[len(out)-1]
		g.Nodes = append(g.Nodes, PortalNode{
			ID: r.ID, Name: r.DisplayName, Region: r.Region, Protocol: r.Protocol, IsServerOnline: r.IsServerOnline,
			ServerLoad: snapshotCPU(r.StatusSnapshot), OnlineClients: int(r.OnlineClients),
		})
	}
	return out, nil
}

// GetNodeLink 返回本人某个节点的分享链接（凭据）。节点不在授权范围或暂无链接返回 404，账号非 active 返回 409。
func (s *UserService) GetNodeLink(ctx context.Context, p *UserPrincipal, nodeID int64) (string, error) {
	if p.Status != UserStatusActive {
		return "", newError(CodeConflict, msgAccountNotActive)
	}
	items, err := s.loadSubItems(ctx, p.UserID)
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if it.nodeID == nodeID {
			return it.link, nil
		}
	}
	return "", newError(CodeNotFound, msgNodeNotFound)
}
