-- v2-hub 建表 SQL（PostgreSQL 17）。hub-server 的 dao 层以本文件为准。
-- 增量变更放 migrations/（幂等，手工执行），并同步回本文件，保证 reset_db.sh 得到最新结构。
-- 业务语义（状态、权限码含义）以 docs/03-业务语义.md 为准。

-- M0：管理员与 RBAC ---------------------------------------------------------

CREATE TABLE admin (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username        VARCHAR(64)  NOT NULL,
    password_hash   VARCHAR(255) NOT NULL,           -- argon2id 编码串，含盐与参数
    totp_secret_enc BYTEA,                           -- AES-GCM 加密后的 TOTP 密钥；NULL = 未启用 2FA（docs/03 §4.3）
    totp_pending_enc BYTEA,                          -- 开启流程中待确认的密钥；确认后转存 totp_secret_enc
    totp_last_step  BIGINT       NOT NULL DEFAULT 0, -- 最近一次成功使用的 TOTP 步长，防重放
    is_disabled     BOOLEAN      NOT NULL DEFAULT FALSE,
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_admin_username UNIQUE (username)
);

CREATE TABLE role (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code        VARCHAR(64)  NOT NULL,               -- 如 super_admin / operator / auditor
    name        VARCHAR(64)  NOT NULL,
    is_builtin  BOOLEAN      NOT NULL DEFAULT FALSE, -- 内置角色不可删除
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_role_code UNIQUE (code)
);

-- 权限码与左侧菜单 / 接口一一对应，取值清单见 docs/03 §权限码表。
CREATE TABLE permission (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code        VARCHAR(128) NOT NULL,               -- 如 server:read / server:write / node:write
    name        VARCHAR(64)  NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_permission_code UNIQUE (code)
);

CREATE TABLE role_permission (
    role_id       BIGINT NOT NULL REFERENCES role (id) ON DELETE CASCADE,
    permission_id BIGINT NOT NULL REFERENCES permission (id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE admin_role (
    admin_id BIGINT NOT NULL REFERENCES admin (id) ON DELETE CASCADE,
    role_id  BIGINT NOT NULL REFERENCES role (id) ON DELETE CASCADE,
    PRIMARY KEY (admin_id, role_id)
);
CREATE INDEX idx_admin_role_role_id ON admin_role (role_id);

-- 管理员会话（docs/03 §4.2）：只存 token 的 SHA-256，明文只在 Cookie 里。
CREATE TABLE admin_session (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash    BYTEA        NOT NULL,
    admin_id      BIGINT       NOT NULL REFERENCES admin (id) ON DELETE CASCADE,
    csrf_token    VARCHAR(64)  NOT NULL,               -- 随机值，经同源响应下发，非凭据
    client_ip     INET,
    user_agent    VARCHAR(255),
    expires_at    TIMESTAMPTZ  NOT NULL,               -- 绝对过期
    idle_until    TIMESTAMPTZ  NOT NULL,               -- 空闲过期，请求时顺延
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_admin_session_token_hash UNIQUE (token_hash)
);
CREATE INDEX idx_admin_session_admin_id ON admin_session (admin_id);
CREATE INDEX idx_admin_session_expires_at ON admin_session (expires_at);

-- 审计日志：只追加，不更新不删除。
CREATE TABLE audit_log (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_type  VARCHAR(16)  NOT NULL,               -- admin / user / system
    actor_id    BIGINT,
    action      VARCHAR(64)  NOT NULL,               -- 如 server.create / inbound.delete
    target_type VARCHAR(32),
    target_id   VARCHAR(64),
    detail      JSONB,                               -- 变更摘要；不得包含凭据与分享链接明文
    client_ip   INET,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_log_created_at ON audit_log (created_at);
CREATE INDEX idx_audit_log_actor ON audit_log (actor_type, actor_id);

-- M1：服务器、入站、Host 端点、节点与节点组（docs/03 §5.1）-----------------------

CREATE TABLE server (
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

CREATE TABLE inbound (
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

CREATE TABLE endpoint (
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

CREATE TABLE node (
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

CREATE TABLE node_group (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       VARCHAR(64)  NOT NULL,
    remark     VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_node_group_name UNIQUE (name)
);

CREATE TABLE node_group_item (
    node_group_id BIGINT NOT NULL REFERENCES node_group (id) ON DELETE CASCADE,
    node_id       BIGINT NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    PRIMARY KEY (node_group_id, node_id)
);
CREATE INDEX idx_node_group_item_node_id ON node_group_item (node_id);

-- 种子数据 ---------------------------------------------------------------------
-- 内置角色 super_admin 隐式拥有全部权限码（docs/03 §4），不写 role_permission。
INSERT INTO role (code, name, is_builtin) VALUES ('super_admin', '超级管理员', TRUE);

-- 权限码清单与 docs/03 §4 权限码表逐行对应。
INSERT INTO permission (code, name) VALUES
    ('admin:read',   '查看管理员与角色'),
    ('admin:write',  '管理管理员与角色'),
    ('audit:read',   '查看审计日志'),
    ('server:read',  '查看服务器'),
    ('server:write', '管理服务器'),
    ('node:read',    '查看节点'),
    ('node:write',   '管理节点');
