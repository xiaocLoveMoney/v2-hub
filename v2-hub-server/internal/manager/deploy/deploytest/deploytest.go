// Package deploytest 提供可编排的假 Deployer，供 service 的单测使用。
package deploytest

import (
	"context"
	"sync"

	"v2-hub-server/internal/manager/deploy"
	"v2-hub-server/internal/manager/remote"
)

// Fake 是假的 deploy.Deployer：各方法返回预设的结果，并记录被调用的方法名。
// 预设字段在开始调用前设好；Errors 按方法名给出要返回的错误。
type Fake struct {
	Detection deploy.Detection
	Panel     deploy.PanelAccess
	Token     string
	CertPin   string
	Diagnosis string
	Login     deploy.PanelLogin
	// InstallLog 是安装或升级时写进日志的一行；为空不写。
	InstallLog string
	Errors     map[string]error

	mu    sync.Mutex
	calls []string
	// installed 记住 Install 之后面板已存在，让后续 Detect 走接管。
	installed bool
}

var _ deploy.Deployer = (*Fake)(nil)

func (f *Fake) enter(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	return f.Errors[method]
}

// Calls 返回按顺序被调用的方法名。
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Detect 见 deploy.Deployer。
func (f *Fake) Detect(context.Context, remote.Runner) (deploy.Detection, error) {
	if err := f.enter("Detect"); err != nil {
		return deploy.Detection{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	det := f.Detection
	if f.installed {
		det.PanelState, det.Plan = deploy.PanelStandard, deploy.PlanAdopt
	}
	return det, nil
}

// Install 见 deploy.Deployer。
func (f *Fake) Install(_ context.Context, _ remote.Runner, _ deploy.InstallOptions, log deploy.Logger) error {
	if f.InstallLog != "" {
		log(f.InstallLog)
	}
	if err := f.enter("Install"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = true
	return nil
}

// Access 见 deploy.Deployer。
func (f *Fake) Access(context.Context, remote.Runner) (deploy.PanelAccess, error) {
	if err := f.enter("Access"); err != nil {
		return deploy.PanelAccess{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Panel, nil
}

// IssueToken 见 deploy.Deployer。
func (f *Fake) IssueToken(context.Context, remote.Runner) (string, error) {
	if err := f.enter("IssueToken"); err != nil {
		return "", err
	}
	return f.Token, nil
}

// EnableTLS 见 deploy.Deployer。成功后 Access 报告面板有证书。
func (f *Fake) EnableTLS(context.Context, remote.Runner, string) (string, error) {
	if err := f.enter("EnableTLS"); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Panel.HasTLS, f.Panel.CertSHA256 = true, f.CertPin
	return f.CertPin, nil
}

// OpenFirewall 见 deploy.Deployer。
func (f *Fake) OpenFirewall(context.Context, remote.Runner, int) error {
	return f.enter("OpenFirewall")
}

// RestartPanel 见 deploy.Deployer。
func (f *Fake) RestartPanel(context.Context, remote.Runner) error { return f.enter("RestartPanel") }

// RestartCore 见 deploy.Deployer。
func (f *Fake) RestartCore(context.Context, remote.Runner) error { return f.enter("RestartCore") }

// Upgrade 见 deploy.Deployer。
func (f *Fake) Upgrade(_ context.Context, _ remote.Runner, log deploy.Logger) error {
	if f.InstallLog != "" {
		log(f.InstallLog)
	}
	return f.enter("Upgrade")
}

// Diagnose 见 deploy.Deployer。
func (f *Fake) Diagnose(context.Context, remote.Runner) (string, error) {
	if err := f.enter("Diagnose"); err != nil {
		return "", err
	}
	return f.Diagnosis, nil
}

// ResetPanelLogin 见 deploy.Deployer。
func (f *Fake) ResetPanelLogin(context.Context, remote.Runner) (deploy.PanelLogin, error) {
	if err := f.enter("ResetPanelLogin"); err != nil {
		return deploy.PanelLogin{}, err
	}
	return f.Login, nil
}
