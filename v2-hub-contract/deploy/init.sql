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
    email                 VARCHAR(254),                      -- 小写，全站唯一；管理员建的用户可以为空（docs/03 §5.8）
    email_verified_at     TIMESTAMPTZ,
    invited_by            BIGINT       REFERENCES end_user (id) ON DELETE SET NULL,
    invite_code_id        BIGINT,                            -- 外键在 invite_code 建表后补（两表互相引用）
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_end_user_username UNIQUE (username),
    CONSTRAINT ck_end_user_email_lower CHECK (email IS NULL OR email = lower(email))
);
CREATE UNIQUE INDEX uk_end_user_email ON end_user (email) WHERE email IS NOT NULL;
CREATE INDEX idx_end_user_invited_by ON end_user (invited_by) WHERE invited_by IS NOT NULL;
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

-- M4：系统设置、邀请码、邮箱验证码、发件箱、公告、教程、共享账号、工单、签到（docs/03 §5.6–§5.11）----------

-- 系统设置（docs/03 §5.6）：键与类型由代码清单固定，库里没有的键取默认值；凭据不进这张表。
CREATE TABLE setting (
    key        VARCHAR(64) PRIMARY KEY,
    value      JSONB       NOT NULL,
    updated_by BIGINT      REFERENCES admin (id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 邀请码（docs/03 §5.8）：owner_user_id 非空 = 用户的个人码（每人一行，重置时原地换码，已用次数不清零）；空 = 管理员发的码。
CREATE TABLE invite_code (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code          VARCHAR(16)  NOT NULL,               -- 10 位大写字母数字（docs/03 §6）
    owner_user_id BIGINT       REFERENCES end_user (id) ON DELETE CASCADE,
    max_uses      INTEGER      NOT NULL DEFAULT 0,     -- 0 不限；个人码不看这一列，取设置 invite.userMaxUses
    used_count    INTEGER      NOT NULL DEFAULT 0,
    expires_at    TIMESTAMPTZ,                         -- NULL = 不过期
    is_disabled   BOOLEAN      NOT NULL DEFAULT FALSE,
    remark        VARCHAR(255) NOT NULL DEFAULT '',
    created_by    BIGINT       REFERENCES admin (id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_invite_code_code UNIQUE (code)
);
CREATE UNIQUE INDEX uk_invite_code_owner ON invite_code (owner_user_id) WHERE owner_user_id IS NOT NULL;

-- 邮箱验证码（docs/03 §5.7）：只存 HMAC-SHA256；同一邮箱同一用途只有最新一条有效。
CREATE TABLE email_code (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email       VARCHAR(254) NOT NULL,                 -- 小写
    purpose     VARCHAR(16)  NOT NULL,                 -- register / reset / bind
    code_hash   BYTEA        NOT NULL,
    attempts    INTEGER      NOT NULL DEFAULT 0,       -- 已校验次数，达到上限即作废
    expires_at  TIMESTAMPTZ  NOT NULL,
    consumed_at TIMESTAMPTZ,                           -- 校验通过或被新码取代的时间
    client_ip   INET,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_email_code_lookup ON email_code (email, purpose, created_at DESC);
CREATE INDEX idx_email_code_created_at ON email_code (created_at);

-- 发件箱（docs/03 §5.7）：正文 AES-GCM（关联数据 mail_outbox:<aad_key>），发送成功或放弃后清空。
CREATE TABLE mail_outbox (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    aad_key         VARCHAR(32)  NOT NULL,             -- 随机值，只作加密关联数据
    recipient       VARCHAR(254) NOT NULL,
    subject         VARCHAR(255) NOT NULL,
    body_enc        BYTEA,                             -- NULL = 已清空
    priority        SMALLINT     NOT NULL DEFAULT 0,   -- 0 验证码类，1 通知，2 群发；小的先发
    status          VARCHAR(16)  NOT NULL DEFAULT 'pending', -- pending / sent / failed
    attempts        INTEGER      NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_error      VARCHAR(255) NOT NULL DEFAULT '',  -- 脱敏后的 SMTP 错误
    sent_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_mail_outbox_queue ON mail_outbox (priority, next_attempt_at) WHERE status = 'pending';
CREATE INDEX idx_mail_outbox_created_at ON mail_outbox (created_at);

-- 公告（docs/03 §5.9）：正文为 Markdown 源文本（Ruling-25）。
CREATE TABLE announcement (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title        VARCHAR(100) NOT NULL,
    content      TEXT         NOT NULL,
    is_pinned    BOOLEAN      NOT NULL DEFAULT FALSE,
    is_popup     BOOLEAN      NOT NULL DEFAULT FALSE,
    published_at TIMESTAMPTZ,                          -- NULL = 草稿
    mailed_at    TIMESTAMPTZ,                          -- 非空 = 已群发过，不能再发
    created_by   BIGINT       REFERENCES admin (id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_announcement_published ON announcement (is_pinned DESC, published_at DESC) WHERE published_at IS NOT NULL;

-- 教程与下载（docs/03 §5.9）。
CREATE TABLE tutorial (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    platform     VARCHAR(16)  NOT NULL,                -- windows / macos / android / ios / linux / router
    client_name  VARCHAR(64)  NOT NULL,
    summary      VARCHAR(255) NOT NULL DEFAULT '',
    links        JSONB        NOT NULL DEFAULT '[]',   -- [{label, url}]，最多 5 个
    content      TEXT         NOT NULL DEFAULT '',
    sort_order   INTEGER      NOT NULL DEFAULT 0,
    is_published BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_tutorial_platform ON tutorial (platform, sort_order);

-- 共享账号（docs/03 §5.9）：口令 AES-GCM，关联数据 shared_account:<id>。
CREATE TABLE shared_account (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name           VARCHAR(64)  NOT NULL,
    category       VARCHAR(32)  NOT NULL,
    account        VARCHAR(255) NOT NULL,
    credential_enc BYTEA,                              -- 建记录后回填（关联数据要用到 id）
    remark         VARCHAR(255) NOT NULL DEFAULT '',
    sort_order     INTEGER      NOT NULL DEFAULT 0,
    is_published   BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT now()
);
-- 没有行 = 所有有套餐的用户可见。
CREATE TABLE shared_account_plan (
    shared_account_id BIGINT NOT NULL REFERENCES shared_account (id) ON DELETE CASCADE,
    plan_id           BIGINT NOT NULL REFERENCES plan (id) ON DELETE CASCADE,
    PRIMARY KEY (shared_account_id, plan_id)
);

-- 工单（docs/03 §5.10）。
CREATE TABLE ticket (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       BIGINT       NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    subject       VARCHAR(100) NOT NULL,
    status        VARCHAR(16)  NOT NULL DEFAULT 'open', -- open / answered / closed
    last_reply_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    closed_at     TIMESTAMPTZ,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_ticket_user ON ticket (user_id, last_reply_at DESC);
CREATE INDEX idx_ticket_status ON ticket (status, last_reply_at DESC);

CREATE TABLE ticket_message (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ticket_id   BIGINT      NOT NULL REFERENCES ticket (id) ON DELETE CASCADE,
    author_kind VARCHAR(8)  NOT NULL,                  -- user / admin
    admin_id    BIGINT      REFERENCES admin (id) ON DELETE SET NULL, -- author_kind = admin 时的作者
    content     TEXT        NOT NULL,                  -- 纯文本
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ticket_message_ticket ON ticket_message (ticket_id, id);

-- 签到记录（docs/03 §5.11）：唯一约束保证每人每天一次（Ruling-26）。
CREATE TABLE checkin_log (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    checkin_date DATE        NOT NULL,                 -- 按设置 site.timezone 的自然日
    reward_bytes BIGINT      NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_checkin_log_user_date UNIQUE (user_id, checkin_date)
);

ALTER TABLE end_user ADD CONSTRAINT end_user_invite_code_id_fkey FOREIGN KEY (invite_code_id) REFERENCES invite_code (id) ON DELETE SET NULL;

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
    ('plan:write',   '管理套餐'),
    ('setting:read',  '查看系统设置'),
    ('setting:write', '修改系统设置'),
    ('content:read',  '查看公告、教程与共享账号'),
    ('content:write', '管理公告、教程与共享账号'),
    ('ticket:read',   '查看工单'),
    ('ticket:write',  '回复与关闭工单');
