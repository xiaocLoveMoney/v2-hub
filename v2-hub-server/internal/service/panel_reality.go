package service

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"v2-hub-server/internal/manager/panel"
)

// REALITY 目标扫描（docs/03 §5.2 REALITY 目标扫描，Ruling-54）：平台代理面板的扫描接口，由面板所在机器去探测。
// 目标只接受“域名或 IP[:端口]”，不接受网段（平台服务器不当扫描器用）；是否内网由面板判断，平台从不开启内网探测。

const (
	maxRealityTargets   = 20
	maxRealityTargetLen = 255
	maxRealityXver      = 2
	maxRealityPort      = 65535

	msgRealityBadTarget   = "目标须为“域名或 IP[:端口]”，不接受网段与路径"
	msgRealityTooMany     = "一次最多探测 20 个目标"
	msgRealityBadSNI      = "SNI 须为域名"
	msgRealityBadXver     = "Xver 只能是 0、1、2"
	msgRealityUnsupported = "这台服务器的面板不支持 REALITY 目标扫描"
)

// realityTargetPattern 匹配“域名 / IPv4 / 方括号里的 IPv6”加可选端口；斜杠（网段、路径）、空白与其它符号都不匹配。
var realityTargetPattern = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?)(:([0-9]{1,5}))?$`)

func validRealityTarget(target string) bool {
	if target == "" || len(target) > maxRealityTargetLen {
		return false
	}
	m := realityTargetPattern.FindStringSubmatch(target)
	if m == nil {
		return false
	}
	if m[4] == "" {
		return true
	}
	port, err := strconv.Atoi(m[4])
	return err == nil && port >= 1 && port <= maxRealityPort
}

func (s *PanelService) realityScanner(ctx context.Context, serverID int64) (panel.RealityScanner, error) {
	p, _, err := s.openEnabledProvider(ctx, serverID)
	if err != nil {
		return nil, err
	}
	scanner, ok := p.(panel.RealityScanner)
	if !ok {
		return nil, newError(CodeConflict, msgRealityUnsupported)
	}
	return scanner, nil
}

// ScanRealityTarget 让这台服务器的面板探测一个 REALITY 目标。目标不可用不算失败：结果的 Feasible 为 false。
// 参数不合法返回 invalid_argument，面板类型不支持返回 conflict。不记审计。
func (s *PanelService) ScanRealityTarget(ctx context.Context, serverID int64, target, sni string, xver int) (*panel.RealityScan, error) {
	target, sni = strings.TrimSpace(target), strings.TrimSpace(sni)
	if !validRealityTarget(target) {
		return nil, newError(CodeInvalidArgument, msgRealityBadTarget)
	}
	if sni != "" && (len(sni) > maxRealityTargetLen || !exitHostnamePattern.MatchString(sni)) {
		return nil, newError(CodeInvalidArgument, msgRealityBadSNI)
	}
	if xver < 0 || xver > maxRealityXver {
		return nil, newError(CodeInvalidArgument, msgRealityBadXver)
	}
	scanner, err := s.realityScanner(ctx, serverID)
	if err != nil {
		return nil, err
	}
	res, err := scanner.ScanRealityTarget(ctx, target, sni, xver)
	if err != nil {
		return nil, panelError(err)
	}
	return &res, nil
}

// ScanRealityTargets 让这台服务器的面板探测一组候选目标并排序；targets 为空时探测面板自己的候选站点。
func (s *PanelService) ScanRealityTargets(ctx context.Context, serverID int64, targets []string) ([]panel.RealityScan, error) {
	if len(targets) > maxRealityTargets {
		return nil, newError(CodeInvalidArgument, msgRealityTooMany)
	}
	cleaned := make([]string, 0, len(targets))
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if !validRealityTarget(t) {
			return nil, newError(CodeInvalidArgument, msgRealityBadTarget)
		}
		cleaned = append(cleaned, t)
	}
	scanner, err := s.realityScanner(ctx, serverID)
	if err != nil {
		return nil, err
	}
	res, err := scanner.ScanRealityTargets(ctx, cleaned)
	if err != nil {
		return nil, panelError(err)
	}
	return res, nil
}
