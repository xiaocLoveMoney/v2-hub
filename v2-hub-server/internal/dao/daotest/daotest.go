// Package daotest 为需要真实 PostgreSQL 的测试提供隔离的库环境：
// 每次调用新建一个随机 schema，执行契约目录的 init.sql，测试结束后整体删除。
package daotest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvTestDatabaseURL 指向可用的测试库；未设置时调用方测试被跳过。
const EnvTestDatabaseURL = "HUB_TEST_DATABASE_URL"

const (
	setupTimeout   = 30 * time.Second
	schemaRandSize = 6
	testMaxConns   = 4
)

// NewPool 返回连到全新 schema 的连接池；未设置 HUB_TEST_DATABASE_URL 时 t.Skip。
func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(EnvTestDatabaseURL)
	if dsn == "" {
		t.Skipf("%s 未设置，跳过真实数据库用例", EnvTestDatabaseURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	schema := randomSchemaName(t)
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer func() {
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	}()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { dropSchema(t, dsn, schema) })

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.MaxConns = testMaxConns
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()
	// 多语句脚本需走 simple protocol
	if _, err := conn.Conn().PgConn().Exec(ctx, readInitSQL(t)).ReadAll(); err != nil {
		t.Fatalf("apply init.sql: %v", err)
	}
	return pool
}

func randomSchemaName(t *testing.T) string {
	t.Helper()
	buf := make([]byte, schemaRandSize)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random schema name: %v", err)
	}
	return "test_" + hex.EncodeToString(buf)
}

func readInitSQL(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate daotest source")
	}
	// internal/dao/daotest → 模块根 → contract（符号链接到契约目录）
	path := filepath.Join(filepath.Dir(self), "..", "..", "..", "contract", "deploy", "init.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read init.sql: %v", err)
	}
	return string(raw)
}

func dropSchema(t *testing.T, dsn, schema string) {
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Errorf("connect for cleanup: %v", err)
		return
	}
	defer func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("close cleanup connection: %v", err)
		}
	}()
	if _, err := conn.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
		t.Errorf("drop schema %s: %v", schema, err)
	}
}
