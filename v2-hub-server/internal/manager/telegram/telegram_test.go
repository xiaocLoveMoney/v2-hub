package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "123456:test-token-value"

func TestSendMessage(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		if got["chat_id"] == "-100" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	c, err := New(Config{Token: testToken, APIBase: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.SendMessage(ctx, "42", strings.Repeat("长", maxTextRunes+10)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if path != "/bot"+testToken+"/sendMessage" || got["chat_id"] != "42" || len([]rune(got["text"].(string))) != maxTextRunes || got["parse_mode"] != nil {
		t.Fatalf("request = %s %v", path, got["chat_id"])
	}
	err = c.SendMessage(ctx, "-100", "x")
	if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("rejected err = %v", err)
	}

	// 连不上时错误里不能出现 token
	dead, err := New(Config{Token: testToken, APIBase: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	err = dead.SendMessage(ctx, "42", "x")
	if !errors.Is(err, ErrUnreachable) || strings.Contains(err.Error(), testToken) {
		t.Fatalf("unreachable err must not leak the token: %v", err)
	}
}

func TestNew(t *testing.T) {
	if c, err := New(Config{}); c != nil || err != nil {
		t.Fatal("empty token means disabled")
	}
	if _, err := New(Config{Token: testToken, APIBase: "ftp://x"}); !errors.Is(err, ErrBadConfig) {
		t.Fatalf("bad base err = %v", err)
	}
	if _, err := New(Config{Token: "a/b"}); !errors.Is(err, ErrBadConfig) {
		t.Fatalf("bad token err = %v", err)
	}
}
