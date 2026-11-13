package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"v2-hub-server/internal/dao/sqlcgen"
	"v2-hub-server/internal/manager/panel"
)

// 下发状态（docs/03 §2）。
const (
	ProvisionPending = "pending"
	ProvisionSynced  = "synced"
	ProvisionFailed  = "failed"
	maxProvisionErr  = 500
	// initialCredVersion 是用户凭据的初始版本（end_user.cred_version 的默认值）。
	initialCredVersion = 1
)

// platformEmailPattern 是平台生成的 client email（docs/03 §6）；对账只管符合它的 client，面板上其它 client 不碰。
var platformEmailPattern = regexp.MustCompile(`^u[0-9]+-[a-z0-9]{6}$`)

var serverLocks sync.Map // serverID → *sync.Mutex：同一台面板的对账与流量采集串行

func lockServer(id int64) func() {
	m, _ := serverLocks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// TriggerReconcile 请求尽快对账全部服务器；非阻塞，短时间内的多次请求合并成一次。
func (s *UserService) TriggerReconcile() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// ReconcileRequests 返回对账请求信号，由定时任务消费（docs/03 §5.2：变更即触发，另定时兜底）。
func (s *UserService) ReconcileRequests() <-chan struct{} {
	return s.trigger
}

// ReconcileUser 立即对账该用户涉及的全部服务器（管理员手动重试）。
func (s *UserService) ReconcileUser(ctx context.Context, id int64) error {
	if _, err := s.GetUser(ctx, id); err != nil {
		return err
	}
	servers, err := s.q.ListUserServerIDs(ctx, id)
	if err != nil {
		return fmt.Errorf("list servers of user %d: %w", id, err)
	}
	s.reconcileServers(ctx, servers)
	return nil
}

func (s *UserService) reconcileServers(ctx context.Context, ids []int64) {
	for _, id := range ids {
		if err := s.ReconcileServer(ctx, id); err != nil {
			slog.Warn("reconcile server", "server_id", id, "err", err)
		}
	}
}

// desiredClient 是一个用户在这台面板上的期望。
type desiredClient struct {
	userID  int64
	email   string
	refs    []string
	enabled bool
	limitIP int
	present bool // false = 应删除（无授权或用户待删除）
	cred    clientCredential
	// credVersion 是用户当前的凭据版本；syncedVersion 是已写到这台面板的版本，较小时须换凭据（docs/03 §5.4 重置）
	credVersion   int32
	syncedVersion int32
	hasLinks      bool
}

// needsRotation 报告面板上的凭据是否落后于用户当前凭据。
func (d desiredClient) needsRotation() bool {
	return d.syncedVersion < d.credVersion
}

func (d desiredClient) spec() panel.ClientSpec {
	spec := panel.ClientSpec{Email: d.email, UUID: d.cred.UUID, LimitIP: d.limitIP, Enabled: d.enabled, InboundRefs: d.refs}
	spec.Auth = d.cred.Auth
	return spec
}

// ReconcileServer 把一台面板上平台管理的 client 对齐到期望状态（docs/03 §5.2 对账，铁律五）。
// 已停用的服务器不碰（T4）。单个用户失败不影响其它用户，结果写 provision。
func (s *UserService) ReconcileServer(ctx context.Context, serverID int64) error {
	unlock := lockServer(serverID)
	defer unlock()
	p, srv, err := s.panels.openProvider(ctx, serverID)
	if err != nil {
		return err
	}
	if srv.IsDisabled {
		return nil
	}
	desired, err := s.loadDesired(ctx, serverID)
	if err != nil {
		return err
	}
	clients, err := p.ListClients(ctx)
	if err != nil {
		return fmt.Errorf("list clients of server %d: %w", serverID, err)
	}
	actual := make(map[string]panel.Client, len(clients))
	for _, c := range clients {
		if platformEmailPattern.MatchString(c.Email) {
			actual[c.Email] = c
		}
	}
	stamp, err := s.q.GetServerLinkFingerprint(ctx, serverID)
	if err != nil {
		return fmt.Errorf("link fingerprint of server %d: %w", serverID, err)
	}
	prevStamp, _ := s.linkStamps.Load(serverID)
	refreshAll := prevStamp != stamp
	failures := make(map[int64]error, len(desired))
	needLinks := make(map[int64]bool, len(desired))
	var toEnable, toDisable []string
	owner := make(map[string]int64, len(desired))
	for _, d := range desired {
		owner[d.email] = d.userID
		cur, exists := actual[d.email]
		delete(actual, d.email)
		var opErr error
		switch {
		case !d.present && exists:
			opErr = p.DeleteClient(ctx, d.email)
		case !d.present:
		case !exists:
			opErr = p.AddClient(ctx, d.spec())
			needLinks[d.userID] = true
		case d.needsRotation():
			// 换凭据时启停与 IP 限制一并写入，不再进批量启停
			if _, opErr = applyMounts(ctx, p, d, cur); opErr == nil {
				opErr = p.SetClientCredential(ctx, d.spec())
			}
			needLinks[d.userID] = true
		default:
			var mounted bool
			mounted, opErr = applyClientDiff(ctx, p, d, cur)
			needLinks[d.userID] = mounted
			if opErr == nil && cur.LimitIP == d.limitIP && cur.Enabled != d.enabled {
				if d.enabled {
					toEnable = append(toEnable, d.email)
				} else {
					toDisable = append(toDisable, d.email)
				}
			}
		}
		if opErr != nil {
			failures[d.userID] = opErr
		}
	}
	// 同一面板的启停合并成两次调用：3x-ui 禁用 client 会重启 Xray（docs/03 §5.2）
	for _, batch := range []struct {
		emails  []string
		enabled bool
	}{{toDisable, false}, {toEnable, true}} {
		if err := p.SetClientsEnabled(ctx, batch.emails, batch.enabled); err != nil {
			for _, e := range batch.emails {
				failures[owner[e]] = err
			}
		}
	}
	// 面板上剩下的平台格式 client 不属于任何期望（平台库重置、用户已删等遗留），一并删除
	for email := range actual {
		if err := p.DeleteClient(ctx, email); err != nil {
			slog.Warn("delete orphan client", "server_id", serverID, "email", email, "err", err)
		}
	}
	links := s.fetchLinks(ctx, p, desired, failures, needLinks, refreshAll)
	if err := s.saveResults(ctx, serverID, desired, failures, links); err != nil {
		return err
	}
	if len(failures) == 0 {
		s.linkStamps.Store(serverID, stamp)
	}
	return nil
}

// fetchLinks 向面板取分享链接（docs/03 §5.4 节点来源）：新建、换凭据、挂载变了、还没有链接的用户，
// 以及入站 / 对外地址变化后的全部用户。取失败记为该用户本轮失败，下轮重试。
func (s *UserService) fetchLinks(ctx context.Context, p panel.Provider, desired []desiredClient, failures map[int64]error, need map[int64]bool, refreshAll bool) map[int64][]string {
	out := make(map[int64][]string, len(desired))
	for _, d := range desired {
		if !d.present || failures[d.userID] != nil {
			continue
		}
		if !refreshAll && d.hasLinks && !need[d.userID] {
			continue
		}
		links, err := p.ClientLinks(ctx, d.email)
		if err != nil {
			failures[d.userID] = err
			continue
		}
		if links == nil {
			links = []string{}
		}
		out[d.userID] = links
	}
	return out
}

// applyClientDiff 补齐挂载差异；IP 限制变了时连同启停一起改（UpdateClient）。返回挂载是否有变化。
func applyClientDiff(ctx context.Context, p panel.Provider, d desiredClient, cur panel.Client) (bool, error) {
	mounted, err := applyMounts(ctx, p, d, cur)
	if err != nil {
		return mounted, err
	}
	if cur.LimitIP != d.limitIP {
		return mounted, p.UpdateClient(ctx, d.spec())
	}
	return mounted, nil
}

// applyMounts 把 client 挂载的入站补齐到期望，返回是否有变化。
func applyMounts(ctx context.Context, p panel.Provider, d desiredClient, cur panel.Client) (bool, error) {
	var attach, detach []string
	for _, r := range d.refs {
		if !slices.Contains(cur.InboundRefs, r) {
			attach = append(attach, r)
		}
	}
	for _, r := range cur.InboundRefs {
		if !slices.Contains(d.refs, r) {
			detach = append(detach, r)
		}
	}
	if len(attach) > 0 {
		if err := p.AttachClient(ctx, d.email, attach); err != nil {
			return false, err
		}
	}
	if len(detach) > 0 {
		if err := p.DetachClient(ctx, d.email, detach); err != nil {
			return len(attach) > 0, err
		}
	}
	return len(attach)+len(detach) > 0, nil
}

// loadDesired 汇总这台面板上每个相关用户的期望：有授权的、已有下发记录的（可能需要删除）。
func (s *UserService) loadDesired(ctx context.Context, serverID int64) ([]desiredClient, error) {
	rows, err := s.q.ListDesiredClients(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list desired clients of server %d: %w", serverID, err)
	}
	provs, err := s.q.ListServerProvisions(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list provisions of server %d: %w", serverID, err)
	}
	refs := make(map[int64][]string, len(rows))
	synced := make(map[int64]sqlcgen.ListServerProvisionsRow, len(provs))
	ids := make([]int64, 0, len(rows)+len(provs))
	for _, r := range rows {
		if _, ok := refs[r.UserID]; !ok {
			ids = append(ids, r.UserID)
		}
		refs[r.UserID] = append(refs[r.UserID], r.RemoteRef)
	}
	for _, v := range provs {
		synced[v.UserID] = v
		if _, ok := refs[v.UserID]; !ok {
			refs[v.UserID] = nil
			ids = append(ids, v.UserID)
		}
	}
	users, err := s.q.ListUsersByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list users for server %d: %w", serverID, err)
	}
	out := make([]desiredClient, 0, len(users))
	for _, u := range users {
		d := desiredClient{
			userID: u.ID, email: clientEmail(u.ID, u.EmailCode), refs: refs[u.ID], enabled: u.Status == UserStatusActive,
			limitIP: int(u.LimitIp), present: !u.IsDeleted && len(refs[u.ID]) > 0,
			credVersion: u.CredVersion, hasLinks: synced[u.ID].HasLinks,
			// 0 是 M3 之前写入的下发记录，面板上是初始凭据，按版本 1 处理
			syncedVersion: max(synced[u.ID].CredVersion, initialCredVersion),
		}
		slices.Sort(d.refs)
		if d.present {
			if d.cred, err = s.openCredential(u.ID, u.ClientCredEnc); err != nil {
				return nil, err
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *UserService) openCredential(userID int64, sealed []byte) (clientCredential, error) {
	plain, err := s.sealer.Open(sealed, credentialAAD(userID))
	if err != nil {
		return clientCredential{}, fmt.Errorf("open credential of user %d: %w", userID, err)
	}
	var c clientCredential
	if err := json.Unmarshal(plain, &c); err != nil {
		return clientCredential{}, fmt.Errorf("decode credential of user %d: %w", userID, err)
	}
	return c, nil
}

func provisionError(err error) string {
	msg := err.Error()
	var pe *panel.Error
	if errors.As(err, &pe) {
		msg = pe.Message
	}
	if utf8.RuneCountInString(msg) > maxProvisionErr {
		msg = string([]rune(msg)[:maxProvisionErr])
	}
	return msg
}

// saveResults 写回 provision：应删除且已删掉的移除记录；其余记 synced 或 failed（重试次数 +1）。最后清理已删完的待删除用户。
func (s *UserService) saveResults(ctx context.Context, serverID int64, desired []desiredClient, failures map[int64]error, links map[int64][]string) error {
	prev, err := s.q.ListServerProvisions(ctx, serverID)
	if err != nil {
		return fmt.Errorf("list provisions of server %d: %w", serverID, err)
	}
	retries := make(map[int64]int32, len(prev))
	for _, v := range prev {
		retries[v.UserID] = v.RetryCount
	}
	now := pgtype.Timestamptz{Time: s.now(), Valid: true}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		for _, d := range desired {
			opErr := failures[d.userID]
			if !d.present && opErr == nil {
				if err := q.DeleteProvision(ctx, sqlcgen.DeleteProvisionParams{UserID: d.userID, ServerID: serverID}); err != nil {
					return fmt.Errorf("delete provision %d/%d: %w", d.userID, serverID, err)
				}
				continue
			}
			params := sqlcgen.UpsertProvisionParams{UserID: d.userID, ServerID: serverID, Email: d.email, Status: ProvisionSynced, SyncedAt: now, CredVersion: d.credVersion}
			if opErr != nil {
				params.Status, params.LastError, params.SyncedAt = ProvisionFailed, provisionError(opErr), pgtype.Timestamptz{}
				params.RetryCount = retries[d.userID] + 1
				slog.Warn("provision failed", "server_id", serverID, "user_id", d.userID, "err", opErr)
			}
			provisionID, err := q.UpsertProvision(ctx, params)
			if err != nil {
				return fmt.Errorf("save provision %d/%d: %w", d.userID, serverID, err)
			}
			if l, ok := links[d.userID]; ok && opErr == nil {
				if err := s.saveLinks(ctx, q, provisionID, l, now); err != nil {
					return err
				}
			}
		}
		if _, err := q.PurgeDeletedUsers(ctx); err != nil {
			return fmt.Errorf("purge deleted users: %w", err)
		}
		return nil
	})
}

func linksAAD(provisionID int64) []byte {
	return fmt.Appendf(nil, "provision:%d:links", provisionID)
}

// saveLinks 把分享链接加密存入下发记录；链接含凭据，不得写日志。
func (s *UserService) saveLinks(ctx context.Context, q *sqlcgen.Queries, provisionID int64, links []string, at pgtype.Timestamptz) error {
	plain, err := json.Marshal(links)
	if err != nil {
		return fmt.Errorf("encode links of provision %d: %w", provisionID, err)
	}
	sealed, err := s.sealer.Seal(plain, linksAAD(provisionID))
	if err != nil {
		return fmt.Errorf("seal links of provision %d: %w", provisionID, err)
	}
	if err := q.SetProvisionLinks(ctx, sqlcgen.SetProvisionLinksParams{ID: provisionID, LinksEnc: sealed, LinksSyncedAt: at}); err != nil {
		return fmt.Errorf("save links of provision %d: %w", provisionID, err)
	}
	return nil
}
