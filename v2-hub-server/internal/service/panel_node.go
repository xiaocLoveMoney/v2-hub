package service

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
)

const (
	maxNodeNameLen    = 128
	maxNodeTags       = 16
	maxNodeTagLen     = 32
	maxNodeSortOrder  = 100000
	maxGroupNameLen   = 64
	maxGroupNodes     = 5000
	msgNodeNotFound   = "节点不存在"
	msgGroupNotFound  = "节点组不存在"
	ActionNodeUpdate  = "node.update"
	ActionGroupCreate = "node_group.create"
	ActionGroupUpdate = "node_group.update"
	ActionGroupDelete = "node_group.delete"
	targetNode        = "node"
	targetNodeGroup   = "node_group"
)

// Node 是节点目录项。
type Node struct {
	ID          int64
	InboundID   int64
	EndpointID  *int64
	ServerID    int64
	ServerName  string
	Protocol    string
	Address     string
	Port        int
	DisplayName string
	Region      string
	Tags        []string
	SortOrder   int
	IsEnabled   bool
	IsMissing   bool
	GroupIDs    []int64
}

// NodeFilter 是节点目录的过滤条件；0 表示不过滤。
type NodeFilter struct {
	ServerID int64
	GroupID  int64
}

// NodeInput 是节点展示属性。
type NodeInput struct {
	DisplayName string
	Region      string
	Tags        []string
	SortOrder   int
	IsEnabled   bool
}

// NodeGroup 是节点组。
type NodeGroup struct {
	ID      int64
	Name    string
	Remark  string
	NodeIDs []int64
}

// NodeGroupInput 是新建 / 修改节点组（成员整体替换）。
type NodeGroupInput struct {
	Name    string
	Remark  string
	NodeIDs []int64
}

func optionalID(id int64) pgtype.Int8 {
	return pgtype.Int8{Int64: id, Valid: id > 0}
}

// ListNodes 分页返回节点目录。
func (s *PanelService) ListNodes(ctx context.Context, f NodeFilter, pq PageQuery) (*Page[Node], error) {
	limit, offset, err := pq.normalize()
	if err != nil {
		return nil, err
	}
	total, err := s.q.CountNodes(ctx, sqlcgen.CountNodesParams{ServerID: optionalID(f.ServerID), GroupID: optionalID(f.GroupID)})
	if err != nil {
		return nil, fmt.Errorf("count nodes: %w", err)
	}
	if total == 0 {
		return &Page[Node]{Items: []Node{}}, nil
	}
	rows, err := s.q.ListNodes(ctx, sqlcgen.ListNodesParams{
		ServerID: optionalID(f.ServerID), GroupID: optionalID(f.GroupID), RowLimit: limit, RowOffset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	items := make([]Node, 0, len(rows))
	for _, r := range rows {
		n := Node{
			ID: r.ID, InboundID: r.InboundID, ServerID: r.ServerID, ServerName: r.ServerName, Protocol: r.Protocol,
			Address: serverHost(r.BaseUrl), Port: int(r.InboundPort), DisplayName: r.DisplayName, Region: r.Region,
			Tags: r.Tags, SortOrder: int(r.SortOrder), IsEnabled: r.IsEnabled, IsMissing: r.IsMissing, GroupIDs: r.GroupIds,
		}
		if r.EndpointID.Valid {
			v := r.EndpointID.Int64
			n.EndpointID = &v
			n.Address = r.EndpointAddress
			if r.EndpointPort > 0 {
				n.Port = int(r.EndpointPort)
			}
		}
		items = append(items, n)
	}
	return &Page[Node]{Items: items, Total: total}, nil
}

func validateNodeInput(in NodeInput) (NodeInput, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if n := utf8.RuneCountInString(in.DisplayName); n < 1 || n > maxNodeNameLen {
		return in, newError(CodeInvalidArgument, fmt.Sprintf("显示名长度须为 1–%d", maxNodeNameLen))
	}
	if utf8.RuneCountInString(in.Region) > maxRegionLen {
		return in, newError(CodeInvalidArgument, "地区过长")
	}
	if len(in.Tags) > maxNodeTags {
		return in, newError(CodeInvalidArgument, fmt.Sprintf("标签最多 %d 个", maxNodeTags))
	}
	tags := make([]string, 0, len(in.Tags))
	for _, t := range in.Tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) > maxNodeTagLen {
			return in, newError(CodeInvalidArgument, "标签过长："+t)
		}
		tags = append(tags, t)
	}
	in.Tags = tags
	if in.SortOrder < -maxNodeSortOrder || in.SortOrder > maxNodeSortOrder {
		return in, newError(CodeInvalidArgument, "排序值超出范围")
	}
	return in, nil
}

// RenameLegacyNodes 把仍是旧默认名（“服务器名 · 入站称呼”）的直连节点改成新默认名（Ruling-63），返回改了几个。
// 启动时调用；管理员改过的名字不以服务器名开头，不受影响。
func (s *PanelService) RenameLegacyNodes(ctx context.Context) (int64, error) {
	n, err := s.q.RenameLegacyDirectNodes(ctx, nodeNameSeparator)
	if err != nil {
		return 0, fmt.Errorf("rename legacy direct nodes: %w", err)
	}
	return n, nil
}

// UpdateNode 修改节点展示属性（只影响平台，不写回面板）。
func (s *PanelService) UpdateNode(ctx context.Context, actor *AdminPrincipal, id int64, in NodeInput, ip *netip.Addr) error {
	in, err := validateNodeInput(in)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.UpdateNode(ctx, sqlcgen.UpdateNodeParams{
			ID: id, DisplayName: in.DisplayName, Region: in.Region, Tags: in.Tags, SortOrder: int32(in.SortOrder), IsEnabled: in.IsEnabled,
		})
		if err != nil {
			return fmt.Errorf("update node %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgNodeNotFound)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionNodeUpdate, TargetType: targetNode,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"displayName": in.DisplayName, "isEnabled": in.IsEnabled},
		})
	})
}

// ListNodeGroups 返回全部节点组。
func (s *PanelService) ListNodeGroups(ctx context.Context) ([]NodeGroup, error) {
	rows, err := s.q.ListNodeGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("list node groups: %w", err)
	}
	out := make([]NodeGroup, 0, len(rows))
	for _, r := range rows {
		out = append(out, NodeGroup{ID: r.ID, Name: r.Name, Remark: r.Remark, NodeIDs: r.NodeIds})
	}
	return out, nil
}

func validateGroupInput(ctx context.Context, q *sqlcgen.Queries, in NodeGroupInput) (NodeGroupInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > maxGroupNameLen {
		return in, newError(CodeInvalidArgument, fmt.Sprintf("名称长度须为 1–%d", maxGroupNameLen))
	}
	if utf8.RuneCountInString(in.Remark) > maxRemarkLen {
		return in, newError(CodeInvalidArgument, "备注过长")
	}
	if len(in.NodeIDs) > maxGroupNodes {
		return in, newError(CodeInvalidArgument, "节点过多")
	}
	unique := make([]int64, 0, len(in.NodeIDs))
	seen := make(map[int64]struct{}, len(in.NodeIDs))
	for _, id := range in.NodeIDs {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	in.NodeIDs = unique
	if len(unique) == 0 {
		return in, nil
	}
	n, err := q.CountNodesByIDs(ctx, unique)
	if err != nil {
		return in, fmt.Errorf("count nodes: %w", err)
	}
	if int(n) != len(unique) {
		return in, newError(CodeInvalidArgument, "包含不存在的节点")
	}
	return in, nil
}

func replaceGroupItems(ctx context.Context, q *sqlcgen.Queries, groupID int64, nodeIDs []int64) error {
	if err := q.ClearNodeGroupItems(ctx, groupID); err != nil {
		return fmt.Errorf("clear node group %d: %w", groupID, err)
	}
	if len(nodeIDs) == 0 {
		return nil
	}
	if err := q.InsertNodeGroupItems(ctx, sqlcgen.InsertNodeGroupItemsParams{GroupID: groupID, NodeIds: nodeIDs}); err != nil {
		return fmt.Errorf("fill node group %d: %w", groupID, err)
	}
	return nil
}

// CreateNodeGroup 新建节点组。
func (s *PanelService) CreateNodeGroup(ctx context.Context, actor *AdminPrincipal, in NodeGroupInput, ip *netip.Addr) (*NodeGroup, error) {
	var created *NodeGroup
	err := s.inTx(ctx, func(q *sqlcgen.Queries) error {
		in, err := validateGroupInput(ctx, q, in)
		if err != nil {
			return err
		}
		id, err := q.CreateNodeGroup(ctx, sqlcgen.CreateNodeGroupParams{Name: in.Name, Remark: in.Remark})
		if isUniqueViolation(err) {
			return newError(CodeConflict, "节点组名称已存在")
		}
		if err != nil {
			return fmt.Errorf("create node group: %w", err)
		}
		if err := replaceGroupItems(ctx, q, id, in.NodeIDs); err != nil {
			return err
		}
		created = &NodeGroup{ID: id, Name: in.Name, Remark: in.Remark, NodeIDs: in.NodeIDs}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionGroupCreate, TargetType: targetNodeGroup,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"name": in.Name, "nodes": len(in.NodeIDs)},
		})
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateNodeGroup 修改节点组（成员整体替换）。
func (s *PanelService) UpdateNodeGroup(ctx context.Context, actor *AdminPrincipal, id int64, in NodeGroupInput, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		in, err := validateGroupInput(ctx, q, in)
		if err != nil {
			return err
		}
		n, err := q.UpdateNodeGroup(ctx, sqlcgen.UpdateNodeGroupParams{ID: id, Name: in.Name, Remark: in.Remark})
		if isUniqueViolation(err) {
			return newError(CodeConflict, "节点组名称已存在")
		}
		if err != nil {
			return fmt.Errorf("update node group %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgGroupNotFound)
		}
		if err := replaceGroupItems(ctx, q, id, in.NodeIDs); err != nil {
			return err
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionGroupUpdate, TargetType: targetNodeGroup,
			TargetID: fmt.Sprint(id), ClientIP: ip, Detail: map[string]any{"name": in.Name, "nodes": len(in.NodeIDs)},
		})
	})
}

// DeleteNodeGroup 删除节点组（不删除节点）。
func (s *PanelService) DeleteNodeGroup(ctx context.Context, actor *AdminPrincipal, id int64, ip *netip.Addr) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.DeleteNodeGroup(ctx, id)
		if err != nil {
			return fmt.Errorf("delete node group %d: %w", id, err)
		}
		if n == 0 {
			return newError(CodeNotFound, msgGroupNotFound)
		}
		return writeAudit(ctx, q, AuditEntry{
			ActorType: ActorAdmin, ActorID: actor.AdminID, Action: ActionGroupDelete, TargetType: targetNodeGroup,
			TargetID: fmt.Sprint(id), ClientIP: ip,
		})
	})
}
