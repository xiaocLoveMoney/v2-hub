package dao

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// envTestDatabaseURL 指向一个可用的测试库；未设置时跳过需要真实数据库的用例。
const envTestDatabaseURL = "HUB_TEST_DATABASE_URL"

func TestNewPoolRejectsEmptyDSN(t *testing.T) {
	if _, err := NewPool(context.Background(), "", PoolConfig{MaxConns: 1}); !errors.Is(err, ErrEmptyDSN) {
		t.Fatalf("NewPool() err = %v, want ErrEmptyDSN", err)
	}
}

func TestNewPoolRejectsMalformedDSN(t *testing.T) {
	if _, err := NewPool(context.Background(), "postgres://%zz", PoolConfig{MaxConns: 1}); err == nil {
		t.Fatal("NewPool() err = nil, want parse error")
	}
}

func TestNewPoolPing(t *testing.T) {
	dsn := os.Getenv(envTestDatabaseURL)
	if dsn == "" {
		t.Skipf("%s 未设置，跳过真实数据库用例", envTestDatabaseURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := NewPool(ctx, dsn, PoolConfig{MaxConns: 2})
	if err != nil {
		t.Fatalf("NewPool() err = %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping() err = %v", err)
	}
}
