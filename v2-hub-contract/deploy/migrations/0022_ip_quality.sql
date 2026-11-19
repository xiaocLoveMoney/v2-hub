-- 0022：服务器 IP 质量检测（docs/03 §5.23，Ruling-81、Ruling-82）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。
-- 服务器 IP 质量检测（docs/03 §5.23，Ruling-81、Ruling-82）：每次检测一行。同一服务器同时只有一条进行中（部分唯一索引）。
-- 摘要列给列表与对比用；result 是完整报告（全是枚举码，带格式版本号）。经纬度与各源分数只在 result 里。
-- 删服务器时它的检测记录一并删除；保留条数与天数由 hub-server 清理。
CREATE TABLE IF NOT EXISTS server_ip_check (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id    BIGINT       NOT NULL REFERENCES server (id) ON DELETE CASCADE,
    status       VARCHAR(16)  NOT NULL DEFAULT 'queued',
    origin       VARCHAR(8)   NOT NULL,                     -- manual / auto
    scope        VARCHAR(8)   NOT NULL,                     -- full / platform
    host_probe   VARCHAR(16)  NOT NULL DEFAULT '',          -- 服务器侧：done / no_host / no_permission / host_unverified / ssh_failed / no_curl
    ip           VARCHAR(45)  NOT NULL DEFAULT '',
    ip_source    VARCHAR(8)   NOT NULL DEFAULT '',          -- host / panel / address
    country      VARCHAR(2)   NOT NULL DEFAULT '',
    reg_country  VARCHAR(2)   NOT NULL DEFAULT '',
    ip_kind      VARCHAR(12)  NOT NULL DEFAULT '',          -- native / broadcast / unknown
    asn          BIGINT,
    as_org       VARCHAR(128) NOT NULL DEFAULT '',
    usage_type   VARCHAR(16)  NOT NULL DEFAULT '',
    risk_level   VARCHAR(12)  NOT NULL DEFAULT '',
    dnsbl_listed INTEGER      NOT NULL DEFAULT 0,
    dnsbl_total  INTEGER      NOT NULL DEFAULT 0,
    media_ok     INTEGER      NOT NULL DEFAULT 0,
    media_total  INTEGER      NOT NULL DEFAULT 0,
    port25       VARCHAR(8)   NOT NULL DEFAULT '',          -- open / blocked / unknown
    result       JSONB,                                     -- 完整报告；还没执行完为 NULL
    error_code   VARCHAR(32)  NOT NULL DEFAULT '',          -- no_public_ip / interrupted / timeout / internal
    last_error   VARCHAR(500) NOT NULL DEFAULT '',
    created_by   BIGINT       REFERENCES admin (id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ,
    CONSTRAINT ck_server_ip_check_status CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    CONSTRAINT ck_server_ip_check_origin CHECK (origin IN ('manual', 'auto')),
    CONSTRAINT ck_server_ip_check_scope CHECK (scope IN ('full', 'platform'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_server_ip_check_active ON server_ip_check (server_id) WHERE status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS idx_server_ip_check_server ON server_ip_check (server_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_server_ip_check_queued ON server_ip_check (id) WHERE status = 'queued';
CREATE INDEX IF NOT EXISTS idx_server_ip_check_created_at ON server_ip_check (created_at);
