package service

import (
	"context"
	"fmt"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 管理端的全站链式规则列表（docs/03 §5.19 管理端，server:read）：只读，不代用户改规则（Ruling-41）。

const msgChainBadKindFilter = "类型须为 client 或 server"

// AdminChainRoute 是管理端全站列表里的一条规则。
type AdminChainRoute struct {
	ChainRoute
	Username string
}

// ChainRouteFilter 是全站规则列表的筛选条件；零值表示不限。ServerID 筛的是“有起飞节点在这台服务器上”。
type ChainRouteFilter struct {
	Kind     string
	UserID   int64
	ServerID int64
}

// ListRoutes 返回全站链式规则，按 id 倒序分页；每条的状态与用户自己看到的一致。
func (s *ChainService) ListRoutes(ctx context.Context, f ChainRouteFilter, pq PageQuery) (*Page[AdminChainRoute], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	isKnownKind := f.Kind == "" || f.Kind == ChainKindClient || f.Kind == ChainKindServer
	if !isKnownKind {
		return nil, newError(CodeInvalidArgument, msgChainBadKindFilter)
	}
	kindArg := textOrNull(f.Kind)
	userArg := optionalID(f.UserID)
	serverArg := optionalID(f.ServerID)
	total, err := s.q.CountAdminChainRoutes(ctx, sqlcgen.CountAdminChainRoutesParams{Kind: kindArg, UserID: userArg, ServerID: serverArg})
	if err != nil {
		return nil, fmt.Errorf("count admin chain routes: %w", err)
	}
	if total == 0 {
		return &Page[AdminChainRoute]{Items: []AdminChainRoute{}}, nil
	}
	rows, err := s.q.ListAdminChainRoutes(ctx, sqlcgen.ListAdminChainRoutesParams{
		Kind: kindArg, UserID: userArg, ServerID: serverArg, RowLimit: limit, RowOffset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list admin chain routes: %w", err)
	}
	coreServers, err := coreConfigServers(ctx, s.q, s.applier)
	if err != nil {
		return nil, err
	}
	// 一条规则的状态取决于同一用户的其它规则（覆盖、重复），所以按用户整体装配
	states := make(map[int64]*chainState, len(rows))
	items := make([]AdminChainRoute, 0, len(rows))
	for _, r := range rows {
		st, ok := states[r.UserID]
		if !ok {
			if st, err = loadChainState(ctx, s.q, s.sealer, coreServers, r.UserID); err != nil {
				return nil, err
			}
			states[r.UserID] = st
		}
		route, ok := st.route(r.ID)
		if !ok {
			continue
		}
		items = append(items, AdminChainRoute{ChainRoute: *route, Username: r.Username})
	}
	return &Page[AdminChainRoute]{Items: items, Total: total}, nil
}
