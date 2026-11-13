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
    online_clients  INTEGER      NOT NULL DEFAULT 0,       -- 面板在线 client 数（docs/03 §5.5）
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
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);
-- 面板会复用已删入站的标识：只要求“未缺失”的记录唯一，缺失记录作为历史保留（docs/03 §5.1 面板标识复用）
CREATE UNIQUE INDEX uk_inbound_server_remote ON inbound (server_id, remote_ref) WHERE NOT is_missing;

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

CREATE TABLE plan (
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

CREATE TABLE plan_node_group (
    plan_id       BIGINT NOT NULL REFERENCES plan (id) ON DELETE CASCADE,
    node_group_id BIGINT NOT NULL REFERENCES node_group (id) ON DELETE CASCADE,
    PRIMARY KEY (plan_id, node_group_id)
);
CREATE INDEX idx_plan_node_group_group_id ON plan_node_group (node_group_id);

-- 终端用户（docs/03 §5.2）。表名避开 PostgreSQL 保留字 user。
CREATE TABLE end_user (
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
    totp_secret_enc       BYTEA,                             -- AES-GCM；NULL = 未启用两步验证（docs/03 §5.3）
    totp_pending_enc      BYTEA,                             -- 开启流程中待确认的密钥
    totp_last_step        BIGINT       NOT NULL DEFAULT 0,   -- 最近一次成功使用的步长，防重放
    sub_token_hash        BYTEA,                             -- 订阅 token 的 SHA-256，用于查找（docs/03 §5.4）
    sub_token_enc         BYTEA,                             -- 订阅 token，AES-GCM，关联数据 end_user:<id>:sub
    cred_version          INTEGER      NOT NULL DEFAULT 1,   -- 连接凭据版本，重置订阅时 +1
    last_used_at          TIMESTAMPTZ,                       -- 最近一次产生流量的时间
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_end_user_username UNIQUE (username)
);
CREATE INDEX idx_end_user_plan_id ON end_user (plan_id);
CREATE UNIQUE INDEX uk_end_user_sub_token_hash ON end_user (sub_token_hash) WHERE sub_token_hash IS NOT NULL;

-- 用户在某台面板上的 client 及对账状态（docs/03 §2 下发状态、§5.2 对账）。
CREATE TABLE provision (
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
    cred_version    INTEGER    NOT NULL DEFAULT 0,           -- 已写到面板的凭据版本；小于用户的即待更新
    links_enc       BYTEA,                                   -- 面板生成的分享链接（JSON 数组），AES-GCM，关联数据 provision:<id>:links
    links_synced_at TIMESTAMPTZ,
    is_online       BOOLEAN    NOT NULL DEFAULT FALSE,       -- 该 client 在面板上是否在线（流量任务顺带采集）
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_provision_user_server UNIQUE (user_id, server_id)
);
CREATE INDEX idx_provision_server_id ON provision (server_id);

-- 按小时的用量明细（客户后台流量明细，M3）。
CREATE TABLE user_traffic_hourly (
    user_id BIGINT      NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    hour    TIMESTAMPTZ NOT NULL,
    up      BIGINT      NOT NULL DEFAULT 0,
    down    BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, hour)
);

-- M3：终端用户会话、登录记录、订阅记录（docs/03 §5.3–§5.5）-------------------------

CREATE TABLE user_session (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash    BYTEA        NOT NULL,
    user_id       BIGINT       NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    csrf_token    VARCHAR(64)  NOT NULL,               -- 随机值，经同源响应下发，非凭据
    client_ip     INET,
    user_agent    VARCHAR(255),
    expires_at    TIMESTAMPTZ  NOT NULL,               -- 绝对过期
    idle_until    TIMESTAMPTZ  NOT NULL,               -- 空闲过期，请求时顺延
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_user_session_token_hash UNIQUE (token_hash)
);
CREATE INDEX idx_user_session_user_id ON user_session (user_id);
CREATE INDEX idx_user_session_expires_at ON user_session (expires_at);

CREATE TABLE user_login_log (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    client_ip  INET,
    is_success BOOLEAN     NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_user_login_log_user_created ON user_login_log (user_id, created_at DESC);
CREATE INDEX idx_user_login_log_created_at ON user_login_log (created_at);

CREATE TABLE subscribe_log (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT       NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    format     VARCHAR(16)  NOT NULL,                  -- base64 / mihomo / singbox / xray
    client_ip  INET,
    user_agent VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_subscribe_log_user_created ON subscribe_log (user_id, created_at DESC);
CREATE INDEX idx_subscribe_log_created_at ON subscribe_log (created_at);

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
    ('node:write',   '管理节点'),
    ('user:read',    '查看用户'),
    ('user:write',   '管理用户'),
    ('plan:read',    '查看套餐'),
    ('plan:write',   '管理套餐');
