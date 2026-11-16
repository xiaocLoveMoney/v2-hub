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
	// Provisioned 是 Provision 的结果；IssueToken 另用 Token。
	Provisioned deploy.Provisioned
	Token       string
	CertPin     string
	Diagnosis   string
	Login       deploy.PanelLogin
	Errors      map[string]error

	mu    sync.Mutex
	calls []string
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
	return f.Detection, nil
}

// Provision 见 deploy.Deployer。
func (f *Fake) Provision(context.Context, remote.Runner, deploy.Detection, deploy.ProvisionOptions, deploy.Logger) (deploy.Provisioned, error) {
	if err := f.enter("Provision"); err != nil {
		return deploy.Provisioned{}, err
	}
	return f.Provisioned, nil
}

// IssueToken 见 deploy.Deployer。
func (f *Fake) IssueToken(context.Context, remote.Runner) (string, error) {
	if err := f.enter("IssueToken"); err != nil {
		return "", err
	}
	return f.Token, nil
}

// EnableTLS 见 deploy.Deployer。
func (f *Fake) EnableTLS(context.Context, remote.Runner, string) (string, error) {
	if err := f.enter("EnableTLS"); err != nil {
		return "", err
	}
	return f.CertPin, nil
}

// RestartPanel 见 deploy.Deployer。
func (f *Fake) RestartPanel(context.Context, remote.Runner) error { return f.enter("RestartPanel") }

// RestartCore 见 deploy.Deployer。
func (f *Fake) RestartCore(context.Context, remote.Runner) error { return f.enter("RestartCore") }

// Upgrade 见 deploy.Deployer。
func (f *Fake) Upgrade(context.Context, remote.Runner, deploy.Logger) error {
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
