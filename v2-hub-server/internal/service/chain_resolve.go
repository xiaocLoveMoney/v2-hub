package service

import (
	"slices"
	"time"

	"v2-hub-server/internal/dao/sqlcgen"
)

// 链式规则的生效计算（docs/03 §5.18、§5.19，Ruling-46）：纯函数，不读库、不连面板。
// 输入是一个用户的全部规则（同类型内已按顺序排好）与当前的授权情况，输出每条规则的状态和每个起飞节点的状态。

// 规则的类型（与 hub-api.yaml 的 ChainKind 对应）。
const (
	ChainKindClient = "client"
	ChainKindServer = "server"
)

// 规则的状态与阻断原因（与 hub-api.yaml 的 ChainStatus、ChainBlockedReason 逐项对应）。
const (
	ChainOK      = "ok"
	ChainBlocked = "blocked"

	ChainReasonNone        = ""
	ChainReasonPlan        = "plan"
	ChainReasonExit        = "exit"
	ChainReasonUnsafe      = "unsafe"
	ChainReasonQuarantined = "quarantined"
)

// 落地的种类（与 hub-api.yaml 的 ChainExitKind 对应）。
const (
	ChainExitNode     = "node"
	ChainExitPlatform = "platform"
	ChainExitOwn      = "own"
)

// 规则里每个起飞节点的状态（与 hub-api.yaml 的 ChainEntryState 逐项对应）。
const (
	EntryInactive    = "inactive"
	EntryOK          = "ok"
	EntryUnavailable = "unavailable"
	EntryDuplicate   = "duplicate"
	EntryShadowed    = "shadowed"
	EntryApplied     = "applied"
	EntryPending     = "pending"
	EntryFailed      = "failed"
)

// ChainRouteEntry 是规则里的一个起飞节点；State 与 CoveredBy 是计算值。
type ChainRouteEntry struct {
	NodeID     int64
	Name       string
	InboundID  int64
	InboundTag string
	ServerID   int64
	ServerName string
	State      string
	// CoveredBy 是覆盖它的规则的序号（State 为 shadowed 或 duplicate 时），否则为 0。
	CoveredBy int
}

// ChainRouteExit 是规则的落地；ID 是平台节点 id 或出口 id，取决于 Kind。
type ChainRouteExit struct {
	Kind     string
	ID       int64
	Name     string
	Protocol string
}

// ChainRoute 是一条链式规则。Position 是在本人同类型规则里的序号（从 1 起连续）；Status、BlockedReason 是计算值。
type ChainRoute struct {
	ID            int64
	UserID        int64
	Kind          string
	Position      int
	Name          string
	DisplayName   string
	Entries       []ChainRouteEntry
	Exit          ChainRouteExit
	Enabled       bool
	Status        string
	BlockedReason string
	CreatedAt     time.Time

	// 以下只在服务内部使用：出口的开关、隔离指纹，以及规则或其出口最近一次改动的时间
	exitEnabled     bool
	exitAllowServer bool
	exitAllowClient bool
	quarantineHash  string
	changedAt       time.Time
}

// applyView 是一台起飞服务器的下发记录里判断状态要用的部分。
type applyView struct {
	status    string
	applied   []int64
	appliedAt time.Time
}

// chainEnv 是生效计算需要的授权与下发情况。
type chainEnv struct {
	flags ChainFlags
	// available 是本人当前可用的平台节点；platformExits 是本人被授权的平台出口（已启用）
	available     map[int64]sqlcgen.ListUserSubNodesRow
	platformExits map[int64]bool
	// coreServers 是面板支持改内核配置的服务器；为 nil 时不计算服务端规则的起飞节点（只关心客户端规则的调用方）
	coreServers map[int64]bool
	// unsafeExits 是没通过服务端链式校验的用户出口；fingerprints 是落地为用户出口的服务端规则当前的内容指纹
	unsafeExits  map[int64]bool
	fingerprints map[int64]string
	views        map[int64]applyView
}

// isKindAllowed 报告套餐是否开了这类链式。
func (f ChainFlags) isKindAllowed(kind string) bool {
	if kind == ChainKindServer {
		return f.ServerChain
	}
	return f.Chain
}

// routeBlockedReason 算一条规则的阻断原因（docs/03 §5.18 规则状态、§5.19 规则状态）；不阻断返回空串。
func routeBlockedReason(r ChainRoute, env chainEnv) string {
	if !env.flags.isKindAllowed(r.Kind) {
		return ChainReasonPlan
	}
	isServer := r.Kind == ChainKindServer
	switch r.Exit.Kind {
	case ChainExitNode:
		exit, isAvailable := env.available[r.Exit.ID]
		if !isAvailable || isQUICProtocol(exit.Protocol) {
			return ChainReasonExit
		}
	case ChainExitPlatform:
		isKindOK := (isServer && r.exitAllowServer) || (!isServer && r.exitAllowClient)
		if !env.platformExits[r.Exit.ID] || !isKindOK {
			return ChainReasonExit
		}
	case ChainExitOwn:
		if !env.flags.CustomNode {
			return ChainReasonPlan
		}
		if !r.exitEnabled {
			return ChainReasonExit
		}
		if isServer && env.unsafeExits[r.Exit.ID] {
			return ChainReasonUnsafe
		}
		isQuarantined := isServer && r.quarantineHash != "" && r.quarantineHash == env.fingerprints[r.ID]
		if isQuarantined {
			return ChainReasonQuarantined
		}
	default:
		return ChainReasonExit
	}
	return ChainReasonNone
}

// serverEntryApplyState 算一个生效的起飞节点的下发情况：规则已在该面板上、且此后没再改动为 applied；
// 否则看这台服务器最近一次下发是否失败。
func serverEntryApplyState(r ChainRoute, view applyView, hasView bool) string {
	isCurrent := hasView && slices.Contains(view.applied, r.ID) && !r.changedAt.After(view.appliedAt)
	if isCurrent {
		return EntryApplied
	}
	if hasView && view.status == chainApplyFailed {
		return EntryFailed
	}
	return EntryPending
}

// isEffectiveEntry 报告一个起飞节点是否生效（客户端：进订阅；服务端：应当在面板上）。
func isEffectiveEntry(state string) bool {
	switch state {
	case EntryOK, EntryApplied, EntryPending, EntryFailed:
		return true
	default:
		return false
	}
}

// clientPair 是客户端链式里的一个（起飞，落地）组合。
type clientPair struct {
	nodeID   int64
	exitKind string
	exitID   int64
}

// resolveRoutes 就地算出一个用户全部规则的状态。routes 必须同类型内按顺序排好；Position 会被改写成从 1 起的连续序号。
//
// 服务端链式：同一个起飞入站只认最靠前的“启用且不阻断”的规则，更靠后的标 shadowed；靠前的规则阻断或停用时不占位，
// 所以自动落到后面的规则。客户端链式：每个（起飞，落地）组合都输出，重复的组合只取第一次。
func resolveRoutes(routes []ChainRoute, env chainEnv) {
	positions := map[string]int{}
	takenInbounds := map[int64]int{}
	seenPairs := map[clientPair]int{}
	for i := range routes {
		r := &routes[i]
		positions[r.Kind]++
		r.Position = positions[r.Kind]
		r.Status, r.BlockedReason = ChainOK, routeBlockedReason(*r, env)
		if r.BlockedReason != ChainReasonNone {
			r.Status = ChainBlocked
		}
		isActive := r.Enabled && r.Status == ChainOK
		for j := range r.Entries {
			e := &r.Entries[j]
			e.State, e.CoveredBy = EntryInactive, 0
			if !isActive {
				continue
			}
			if r.Kind == ChainKindServer {
				resolveServerEntry(r, e, env, takenInbounds)
				continue
			}
			resolveClientEntry(r, e, env, seenPairs)
		}
	}
}

func resolveServerEntry(r *ChainRoute, e *ChainRouteEntry, env chainEnv, taken map[int64]int) {
	_, isAvailable := env.available[e.NodeID]
	if !isAvailable || !env.coreServers[e.ServerID] {
		e.State = EntryUnavailable
		return
	}
	// 同一条规则里两个节点共用一个入站时都算生效（路由只按入站匹配，结果相同）
	if owner, isTaken := taken[e.InboundID]; isTaken && owner != r.Position {
		e.State, e.CoveredBy = EntryShadowed, owner
		return
	}
	taken[e.InboundID] = r.Position
	view, hasView := env.views[e.ServerID]
	e.State = serverEntryApplyState(*r, view, hasView)
}

func resolveClientEntry(r *ChainRoute, e *ChainRouteEntry, env chainEnv, seen map[clientPair]int) {
	if _, isAvailable := env.available[e.NodeID]; !isAvailable {
		e.State = EntryUnavailable
		return
	}
	pair := clientPair{nodeID: e.NodeID, exitKind: r.Exit.Kind, exitID: r.Exit.ID}
	if owner, isSeen := seen[pair]; isSeen {
		e.State, e.CoveredBy = EntryDuplicate, owner
		return
	}
	seen[pair] = r.Position
	e.State = EntryOK
}
