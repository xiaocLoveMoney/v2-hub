package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"v2-hub-server/internal/manager/panel"
	"v2-hub-server/internal/manager/render"
)

// 出口延迟测试（docs/03 §5.21 延迟测试，Ruling-52）：手动发起，由一台起飞面板去连出口，hub-server 自己不连。
// 本文件不写日志：出站配置含凭据。

const (
	defaultExitTestLimit  = 6
	defaultExitTestWindow = time.Minute
	// exitTestTimeout 是一次测试的总时限，比面板自己的探测时限略长。
	exitTestTimeout = 35 * time.Second
	// exitTestTag 是测试用出站的 tag：面板要求出站带 tag，不与平台条目的 hub-r 前缀相同。
	exitTestTag = "hub-test"

	msgExitTestBadMode     = "测试方式只能是 tcp、http 或 real"
	msgExitTestBadNode     = "起点节点不可用"
	msgExitTestUnsupported = "这台服务器的面板不支持延迟测试"
	msgExitTestBusy        = "这台服务器正在进行另一项测试，请稍后再试"
	msgExitTestRateLimited = "测试太频繁，请稍后再试"
	msgExitTestDisabled    = "出口已被管理员停用，不能测试"
	msgExitTestNotAllowed  = "当前套餐不支持自己的出口"
)

// ExitTestResult 是一次出口延迟测试的结果。Error 是面板给出的原因，只给管理员（用户侧恒为空）。
type ExitTestResult struct {
	OK           bool
	Mode         string
	DelayMs      int64
	HTTPStatus   int
	EgressIP     string
	EgressRegion string
	Error        string
}

// exitTestLocks 保证同一台服务器同时只跑一个测试（面板一次只允许一项 HTTP 探测）。
type exitTestLocks struct {
	mu   sync.Mutex
	busy map[int64]bool
}

func newExitTestLocks() *exitTestLocks {
	return &exitTestLocks{busy: make(map[int64]bool, 8)}
}

func (l *exitTestLocks) tryLock(serverID int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy[serverID] {
		return false
	}
	l.busy[serverID] = true
	return true
}

func (l *exitTestLocks) unlock(serverID int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.busy, serverID)
}

func exitTestMode(mode string) (panel.OutboundTestMode, error) {
	m := panel.OutboundTestMode(mode)
	switch m {
	case panel.OutboundTestTCP, panel.OutboundTestHTTP, panel.OutboundTestReal:
		return m, nil
	default:
		return "", newError(CodeInvalidArgument, msgExitTestBadMode)
	}
}

func (s *ChainService) allowExitTest(userID int64) error {
	key := strconv.FormatInt(userID, 10)
	if s.exitTests.IsLocked(key) {
		return newError(CodeRateLimited, msgExitTestRateLimited)
	}
	s.exitTests.RecordFailure(key)
	return nil
}

// testableNode 返回出口用于测试时的出站参数。用户出口（平台出口不受限）先过服务端链式的同一套限制：
// 字段白名单、公网地址并固定成 IP、不指向平台自己的服务器；固定的地址只用于这一次，不落库。
func (s *ChainService) testableNode(ctx context.Context, sec exitSecret) (render.ProxyNode, error) {
	if !sec.isParsed {
		return render.ProxyNode{}, newError(CodeInvalidArgument, msgExitUnsupported)
	}
	isOwn := sec.ownerID != 0
	if !isOwn {
		return sec.node, nil
	}
	ip, err := s.pinExitNode(ctx, sec.node, sec.pinned)
	if err != nil {
		return render.ProxyNode{}, err
	}
	return pinnedExit(sec.node, ip), nil
}

// runExitTest 在 serverID 的面板上测一次 node；同一台服务器已有测试在跑返回 conflict。
func (s *ChainService) runExitTest(ctx context.Context, serverID int64, node render.ProxyNode, mode panel.OutboundTestMode) (*ExitTestResult, error) {
	outbound, ok := render.XrayOutbound(node, exitTestTag)
	if !ok {
		return nil, newError(CodeInvalidArgument, msgExitUnsupported)
	}
	raw, err := json.Marshal(outbound)
	if err != nil {
		return nil, fmt.Errorf("encode test outbound: %w", err)
	}
	if s.applier == nil {
		return nil, newError(CodeConflict, msgExitTestUnsupported)
	}
	prov, _, err := s.applier.panels.openEnabledProvider(ctx, serverID)
	if err != nil {
		return nil, err
	}
	tester, ok := prov.(panel.OutboundTester)
	if !ok {
		return nil, newError(CodeConflict, msgExitTestUnsupported)
	}
	if !s.testing.tryLock(serverID) {
		return nil, newError(CodeConflict, msgExitTestBusy)
	}
	defer s.testing.unlock(serverID)
	ctx, cancel := context.WithTimeout(ctx, exitTestTimeout)
	defer cancel()
	res, err := tester.TestOutbound(ctx, raw, mode)
	if err != nil {
		return nil, panelError(err)
	}
	return &ExitTestResult{
		OK: res.OK, Mode: string(res.Mode), DelayMs: res.DelayMs, HTTPStatus: res.HTTPStatus,
		EgressIP: res.EgressIP, EgressRegion: res.EgressRegion, Error: res.Message,
	}, nil
}

// TestExit 从本人可用的一个节点所在服务器测试本人的出口（R8：他人的与平台出口按不存在处理）。
// 结果不带面板的错误原文。
func (s *ChainService) TestExit(ctx context.Context, p *UserPrincipal, exitID, nodeID int64, mode string) (*ExitTestResult, error) {
	m, err := exitTestMode(mode)
	if err != nil {
		return nil, err
	}
	secrets, err := loadExitSecrets(ctx, s.q, s.sealer, []int64{exitID})
	if err != nil {
		return nil, err
	}
	sec, ok := secrets[exitID]
	if !ok || sec.ownerID != p.UserID {
		return nil, newError(CodeNotFound, msgExitNotFound)
	}
	flags, err := chainFlags(ctx, s.q, p.UserID)
	if err != nil {
		return nil, err
	}
	if !flags.CustomNode {
		return nil, newError(CodeFeatureDisabled, msgExitTestNotAllowed)
	}
	if !sec.enabled {
		return nil, newError(CodeConflict, msgExitTestDisabled)
	}
	_, nodes, err := availableNodes(ctx, s.q, p.UserID)
	if err != nil {
		return nil, err
	}
	entry, ok := nodes[nodeID]
	if !ok {
		return nil, newError(CodeInvalidArgument, msgExitTestBadNode)
	}
	if err := s.allowExitTest(p.UserID); err != nil {
		return nil, err
	}
	node, err := s.testableNode(ctx, sec)
	if err != nil {
		return nil, err
	}
	res, err := s.runExitTest(ctx, entry.ServerID, node, m)
	if err != nil {
		return nil, err
	}
	res.Error = ""
	return res, nil
}

// TestExitAsAdmin 从任一启用的服务器测试任一出口（node:write）；用户出口同样先过地址限制。结果带面板的错误原文。
func (s *ChainService) TestExitAsAdmin(ctx context.Context, exitID, serverID int64, mode string) (*ExitTestResult, error) {
	m, err := exitTestMode(mode)
	if err != nil {
		return nil, err
	}
	if serverID <= 0 {
		return nil, newError(CodeInvalidArgument, msgServerNotFound)
	}
	secrets, err := loadExitSecrets(ctx, s.q, s.sealer, []int64{exitID})
	if err != nil {
		return nil, err
	}
	sec, ok := secrets[exitID]
	if !ok {
		return nil, newError(CodeNotFound, msgExitNotFound)
	}
	node, err := s.testableNode(ctx, sec)
	if err != nil {
		return nil, err
	}
	return s.runExitTest(ctx, serverID, node, m)
}
