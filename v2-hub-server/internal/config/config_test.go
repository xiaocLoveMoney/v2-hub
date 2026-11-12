package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub-server.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantListen string
		wantRead   time.Duration
		wantErr    error
	}{
		{name: "defaults for empty file", content: "", wantListen: defaultListenAddr, wantRead: defaultReadTimeout},
		{name: "override listen", content: "server:\n  listenAddr: 127.0.0.1:9090\n", wantListen: "127.0.0.1:9090", wantRead: defaultReadTimeout},
		{name: "reject empty listen", content: "server:\n  listenAddr: \"\"\n", wantErr: ErrInvalidConfig},
		{name: "reject non-positive timeout", content: "server:\n  readTimeout: 0s\n", wantErr: ErrInvalidConfig},
		{name: "reject non-positive max conns", content: "database:\n  maxConns: 0\n", wantErr: ErrInvalidConfig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tt.content))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Load() err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() unexpected err: %v", err)
			}
			if cfg.Server.ListenAddr != tt.wantListen || cfg.Server.ReadTimeout != tt.wantRead {
				t.Fatalf("Load() = %+v", cfg.Server)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load() err = %v, want os.ErrNotExist", err)
	}
}
