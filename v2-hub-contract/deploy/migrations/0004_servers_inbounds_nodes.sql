-- 0004：M1 服务器、入站、Host 端点、节点与节点组（docs/03 §2、§3 T1–T4、§5.1）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。

CREATE TABLE IF NOT EXISTS server (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name            VARCHAR(64)  NOT NULL,
    provider_type   VARCHAR(16)  NOT NULL,                 -- 面板实现：xui
    base_url        VARCHAR(512) NOT NULL,                 -- 面板根地址，含 scheme、端口、web base path
    api_token_enc   BYTEA        NOT NULL,                 -- AES-256-GCM，关联数据 server:<id>:token
    tls_mode        VARCHAR(8)   NOT NULL DEFAULT 'verify',-- verify / pin / skip
    cert_pin_sha256 VARCHAR(128),                          -- tls_mode=pin 时的叶子证书 SHA-256（Base64）
    region          VARCHAR(64)  NOT NULL DEFAULT '',
    remark          VARCHAR(255) NOT NULL DEFAULT '',
    is_disabled     BOOLEAN      NOT NULL DEFAULT FALSE,
    status          VARCHAR(16)  NOT NULL DEFAULT 'unknown', -- unknown / online / offline / auth_failed
    last_error      VARCHAR(255) NOT NULL DEFAULT '',
    last_seen_at    TIMESTAMPTZ,
    panel_version   VARCHAR(32)  NOT NULL DEFAULT '',
    panel_guid      VARCHAR(64)  NOT NULL DEFAULT '',
    status_snapshot JSONB,                                 -- 最近一次探测的资源快照（CPU、内存、在线时长等）
    last_synced_at  TIMESTAMPTZ,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_server_name UNIQUE (name)
);

CREATE TABLE IF NOT EXISTS inbound (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id         BIGINT       NOT NULL REFERENCES server (id) ON DELETE CASCADE,
    remote_ref        VARCHAR(64)  NOT NULL,               -- 面板上的入站标识（3x-ui 为数字 id）
    protocol          VARCHAR(32)  NOT NULL,
    port              INTEGER      NOT NULL,
    listen            VARCHAR(64)  NOT NULL DEFAULT '',
    tag               VARCHAR(128) NOT NULL DEFAULT '',
    remark            VARCHAR(255) NOT NULL DEFAULT '',
    is_enabled        BOOLEAN      NOT NULL DEFAULT TRUE,
    network           VARCHAR(32)  NOT NULL DEFAULT '',
    security          VARCHAR(32)  NOT NULL DEFAULT '',
    core_type         VARCHAR(16)  NOT NULL DEFAULT 'xray',
    config            JSONB        NOT NULL,               -- 面板配置副本，已删除敏感字段
    client_count      INTEGER      NOT NULL DEFAULT 0,
    synced_hash       VARCHAR(64)  NOT NULL DEFAULT '',    -- 平台最近一次写入或确认时的配置摘要
    drift_detected_at TIMESTAMPTZ,
    is_missing        BOOLEAN      NOT NULL DEFAULT FALSE, -- 面板上已不存在
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_inbound_server_remote UNIQUE (server_id, remote_ref)
);

CREATE TABLE IF NOT EXISTS endpoint (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    inbound_id       BIGINT       NOT NULL REFERENCES inbound (id) ON DELETE CASCADE,
    remote_group_ref VARCHAR(64)  NOT NULL,                -- 面板上的 Host 组标识
    address          VARCHAR(255) NOT NULL,                -- 对外地址（域名或 IP）
    port             INTEGER      NOT NULL DEFAULT 0,      -- 0 表示沿用入站端口
    remark           VARCHAR(255) NOT NULL DEFAULT '',
    is_disabled      BOOLEAN      NOT NULL DEFAULT FALSE,
    is_missing       BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_endpoint UNIQUE (inbound_id, remote_group_ref, address)
);

CREATE TABLE IF NOT EXISTS node (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    inbound_id   BIGINT       NOT NULL REFERENCES inbound (id) ON DELETE CASCADE,
    endpoint_id  BIGINT       REFERENCES endpoint (id) ON DELETE CASCADE, -- NULL = 直连节点
    display_name VARCHAR(128) NOT NULL,
    region       VARCHAR(64)  NOT NULL DEFAULT '',
    tags         TEXT[]       NOT NULL DEFAULT '{}',
    sort_order   INTEGER      NOT NULL DEFAULT 0,
    is_enabled   BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_node_source UNIQUE NULLS NOT DISTINCT (inbound_id, endpoint_id)
);

CREATE TABLE IF NOT EXISTS node_group (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       VARCHAR(64)  NOT NULL,
    remark     VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_node_group_name UNIQUE (name)
);

CREATE TABLE IF NOT EXISTS node_group_item (
    node_group_id BIGINT NOT NULL REFERENCES node_group (id) ON DELETE CASCADE,
    node_id       BIGINT NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    PRIMARY KEY (node_group_id, node_id)
);
CREATE INDEX IF NOT EXISTS idx_node_group_item_node_id ON node_group_item (node_id);
