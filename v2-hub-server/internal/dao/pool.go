// Package dao 是 hub-server 的数据访问层，只做与 PostgreSQL 的交互。
// 表结构以 contract/deploy/init.sql 为准，本层不建表（Ruling-9 允许外键）。
package dao

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrEmptyDSN 表示未提供数据库连接串。
var ErrEmptyDSN = errors.New("database dsn is empty")

// PoolConfig 是连接池参数（不含凭据，凭据在 DSN 里，由环境变量注入）。
type PoolConfig struct {
	// MaxConns 连接池最大连接数，必须为正。
	MaxConns int32
}

// NewPool 按 dsn 创建连接池。pgxpool 惰性建连，这里不探测数据库是否可达，
// 可达性由 /readyz 反映，避免数据库短暂不可用时进程起不来。
func NewPool(ctx context.Context, dsn string, cfg PoolConfig) (*pgxpool.Pool, error) {
	if dsn == "" {
		return nil, ErrEmptyDSN
	}
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// 不把 dsn 拼进错误：其中含密码
		return nil, fmt.Errorf("parse database dsn: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxConns
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	return pool, nil
}
