-- 0006：M2 用户、套餐、下发与用量（docs/03 §2、§3 U1–U9、§5.2）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。

CREATE TABLE IF NOT EXISTS plan (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name               VARCHAR(64)  NOT NULL,
    traffic_limit      BIGINT       NOT NULL DEFAULT 0,       -- 流量额度（字节），0 不限
    duration_days      INTEGER      NOT NULL DEFAULT 0,       -- 时长（天），0 不限
    start_on_first_use BOOLEAN      NOT NULL DEFAULT FALSE,   -- 首次使用后才开始计时（docs/03 U9）
    limit_ip           INTEGER      NOT NULL DEFAULT 0,       -- 同时在线 IP 数，0 不限
    renew_every_days   INTEGER      NOT NULL DEFAULT 0,       -- 自动续期：每 N 天（与下两项三选一）
    renew_month_day    INTEGER      NOT NULL DEFAULT 0,       -- 自动续期：每月几号
    renew_weekday      INTEGER      NOT NULL DEFAULT 0,       -- 自动续期：每周几（1–7 = 周一至周日）
    renew_max          INTEGER      NOT NULL DEFAULT 0,       -- 自动续期最多次数，0 不限
    traffic_reset      VARCHAR(8)   NOT NULL DEFAULT 'never', -- 周期流量重置：never / hourly / daily / weekly / monthly
    traffic_reset_day  INTEGER      NOT NULL DEFAULT 1,       -- 按月重置的日期（1–31）
    allow_client_chain BOOLEAN      NOT NULL DEFAULT FALSE,   -- 功能开关：客户端链式（M4）
    allow_custom_node  BOOLEAN      NOT NULL DEFAULT FALSE,   -- 功能开关：自有落地节点（M4）
    remark             VARCHAR(255) NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_plan_name UNIQUE (name)
);

CREATE TABLE IF NOT EXISTS plan_node_group (
    plan_id       BIGINT NOT NULL REFERENCES plan (id) ON DELETE CASCADE,
    node_group_id BIGINT NOT NULL REFERENCES node_group (id) ON DELETE CASCADE,
    PRIMARY KEY (plan_id, node_group_id)
);
CREATE INDEX IF NOT EXISTS idx_plan_node_group_group_id ON plan_node_group (node_group_id);

-- 终端用户（docs/03 §5.2）。表名避开 PostgreSQL 保留字 user。
CREATE TABLE IF NOT EXISTS end_user (
    id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username              VARCHAR(64)  NOT NULL,
    password_hash         VARCHAR(255) NOT NULL,
    remark                VARCHAR(255) NOT NULL DEFAULT '',
    plan_id               BIGINT       REFERENCES plan (id) ON DELETE SET NULL,
    traffic_limit         BIGINT       NOT NULL DEFAULT 0,   -- 字节，0 不限
    traffic_used          BIGINT       NOT NULL DEFAULT 0,   -- 上行 + 下行（字节）
    expires_at            TIMESTAMPTZ,                       -- NULL = 不过期（或等待首次使用）
    pending_days          INTEGER      NOT NULL DEFAULT 0,   -- >0：首次使用后从那一刻起算的天数（docs/03 U9）
    limit_ip              INTEGER      NOT NULL DEFAULT 0,
    renew_every_days      INTEGER      NOT NULL DEFAULT 0,
    renew_month_day       INTEGER      NOT NULL DEFAULT 0,
    renew_weekday         INTEGER      NOT NULL DEFAULT 0,
    renew_max             INTEGER      NOT NULL DEFAULT 0,
    renew_count           INTEGER      NOT NULL DEFAULT 0,
    traffic_reset         VARCHAR(8)   NOT NULL DEFAULT 'never',
    traffic_reset_day     INTEGER      NOT NULL DEFAULT 1,
    last_traffic_reset_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    is_disabled           BOOLEAN      NOT NULL DEFAULT FALSE,
    status                VARCHAR(16)  NOT NULL DEFAULT 'active', -- active / limited / expired / disabled（docs/03 §2）
    email_code            VARCHAR(6)   NOT NULL,             -- 面板 client email 的随机短码：u{id}-{code}（docs/03 §6）
    client_cred_enc       BYTEA        NOT NULL,             -- 面板凭据（UUID、口令）AES-256-GCM，关联数据 end_user:<id>:client
    deleted_at            TIMESTAMPTZ,                       -- 非空 = 待删除：各面板 client 删完后才删记录
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_end_user_username UNIQUE (username)
);
CREATE INDEX IF NOT EXISTS idx_end_user_plan_id ON end_user (plan_id);

-- 用户在某台面板上的 client 及对账状态（docs/03 §2 下发状态、§5.2 对账）。
CREATE TABLE IF NOT EXISTS provision (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       BIGINT       NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    server_id     BIGINT       NOT NULL REFERENCES server (id) ON DELETE CASCADE,
    email         VARCHAR(64)  NOT NULL,
    status        VARCHAR(16)  NOT NULL DEFAULT 'pending',   -- pending / synced / failed
    last_error    VARCHAR(512) NOT NULL DEFAULT '',
    retry_count   INTEGER      NOT NULL DEFAULT 0,
    last_up       BIGINT       NOT NULL DEFAULT 0,           -- 上次读到的面板上行计数，用于求增量
    last_down     BIGINT       NOT NULL DEFAULT 0,
    synced_at     TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_provision_user_server UNIQUE (user_id, server_id)
);
CREATE INDEX IF NOT EXISTS idx_provision_server_id ON provision (server_id);

-- 按小时的用量明细（客户后台流量明细，M3）。
CREATE TABLE IF NOT EXISTS user_traffic_hourly (
    user_id BIGINT      NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    hour    TIMESTAMPTZ NOT NULL,
    up      BIGINT      NOT NULL DEFAULT 0,
    down    BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, hour)
);

INSERT INTO permission (code, name) VALUES
    ('user:read',  '查看用户'),
    ('user:write', '管理用户'),
    ('plan:read',  '查看套餐'),
    ('plan:write', '管理套餐')
ON CONFLICT (code) DO NOTHING;
