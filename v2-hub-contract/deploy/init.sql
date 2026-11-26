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
    is_owner        BOOLEAN      NOT NULL DEFAULT FALSE, -- 所有者（Ruling-99）：至多一个、须为启用的 super_admin，只能在服务器上指定
    kind            VARCHAR(8)   NOT NULL DEFAULT 'staff', -- 账号类型（Ruling-101）：staff 员工 / agent 代理，创建后不可改
    CONSTRAINT uk_admin_username UNIQUE (username),
    CONSTRAINT ck_admin_owner_enabled CHECK (NOT (is_owner AND is_disabled)),
    CONSTRAINT ck_admin_kind CHECK (kind IN ('staff', 'agent')),
    CONSTRAINT ck_admin_owner_staff CHECK (NOT (is_owner AND kind = 'agent'))
);
CREATE UNIQUE INDEX uk_admin_owner ON admin (is_owner) WHERE is_owner;

CREATE TABLE role (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code        VARCHAR(64)  NOT NULL,               -- 内置角色见种子段；自定义角色由超管创建
    name        VARCHAR(64)  NOT NULL,
    is_builtin  BOOLEAN      NOT NULL DEFAULT FALSE, -- 内置角色（super_admin 与四个预设，Ruling-95）不可改不可删，权限由代码定义
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
    reauth_until  TIMESTAMPTZ,                         -- 二次验证有效期（Ruling-100）；NULL 或已过期 = 高危操作前须重新验证
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
    region          VARCHAR(2)   NOT NULL DEFAULT '',      -- 位置：ISO 3166-1 两位国家码，空串为未知（Ruling-65）
    remark          VARCHAR(255) NOT NULL DEFAULT '',
    is_disabled     BOOLEAN      NOT NULL DEFAULT FALSE,
    status          VARCHAR(16)  NOT NULL DEFAULT 'unknown', -- unknown / online / offline / auth_failed
    last_error      VARCHAR(255) NOT NULL DEFAULT '',
    last_seen_at    TIMESTAMPTZ,
    panel_version   VARCHAR(32)  NOT NULL DEFAULT '',
    panel_guid      VARCHAR(64)  NOT NULL DEFAULT '',
    status_snapshot JSONB,                                 -- 最近一次探测成功的资源快照，平台的中立模型（Ruling-83）
    online_clients  INTEGER      NOT NULL DEFAULT 0,       -- 面板在线 client 数（docs/03 §5.5）
    last_synced_at  TIMESTAMPTZ,
    offline_since   TIMESTAMPTZ,                           -- 离开 online 的时刻；online 时为空（docs/03 §5.20 掉线告警）
    alert_sent_at   TIMESTAMPTZ,                           -- 这次掉线已发告警的时刻；未发为空
    client_flow     VARCHAR(32)  NOT NULL DEFAULT '',      -- VLESS 流控（Ruling-77）：空串为不带；打开后平台下发的 client 带这个 flow
    public_ipv4     VARCHAR(45)  NOT NULL DEFAULT '',      -- 面板自报的公网 IPv4，没报为空串（Ruling-83）
    public_ipv6     VARCHAR(45)  NOT NULL DEFAULT '',      -- 面板自报的公网 IPv6，没报为空串
    price_cents     INTEGER,                               -- 价格，整数分；空 = 没填（Ruling-84）
    price_currency  VARCHAR(3)   NOT NULL DEFAULT 'USD',   -- 币种，三位大写字母
    billing_cycle   VARCHAR(16)  NOT NULL DEFAULT 'month', -- month / quarter / half_year / year / once
    expires_on      DATE,                                  -- 到期日（站点时区）；空 = 没填
    traffic_quota_bytes  BIGINT,                           -- 每月流量额度；空 = 不限或没填
    traffic_reset_day    SMALLINT    NOT NULL DEFAULT 1,   -- 每月几号重置（1–31，当月没有这一天取月末）
    traffic_mode         VARCHAR(8)  NOT NULL DEFAULT 'both', -- 计量方式：both 收发合计 / out 只算出站 / max 收发取大
    traffic_adjust_bytes BIGINT      NOT NULL DEFAULT 0,   -- 校准值：管理员填的本周期已用 − 当时测得值
    traffic_adjust_cycle DATE,                             -- 校准值所属周期的起始日；与当前周期不同即作废
    nic_last_sent   BIGINT,                                -- 上次探测时面板报的网卡累计发送字节；空 = 还没有基线
    nic_last_recv   BIGINT,                                -- 上次探测时面板报的网卡累计接收字节
    latency_checked_at TIMESTAMPTZ,                        -- 最近一次三网延迟检测的时间（含失败，Ruling-86）
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_server_name UNIQUE (name),
    CONSTRAINT ck_server_region CHECK (region ~ '^([A-Z]{2})?$'),
    CONSTRAINT ck_server_client_flow CHECK (client_flow IN ('', 'xtls-rprx-vision')),
    CONSTRAINT ck_server_price_cents CHECK (price_cents IS NULL OR price_cents BETWEEN 0 AND 99999999),
    CONSTRAINT ck_server_price_currency CHECK (price_currency ~ '^[A-Z]{3}$'),
    CONSTRAINT ck_server_billing_cycle CHECK (billing_cycle IN ('month', 'quarter', 'half_year', 'year', 'once')),
    CONSTRAINT ck_server_traffic_quota CHECK (traffic_quota_bytes IS NULL OR traffic_quota_bytes >= 0),
    CONSTRAINT ck_server_traffic_reset_day CHECK (traffic_reset_day BETWEEN 1 AND 31),
    CONSTRAINT ck_server_traffic_mode CHECK (traffic_mode IN ('both', 'out', 'max'))
);

-- 服务器采样：健康探测成功时按 5 分钟对齐记一条，同一时段只留最后一次，保留 7 天（docs/03 §5.1，Ruling-65、Ruling-85）。
-- 除 cpu 与 online_clients 外的列是 Ruling-85 加的，之前的行为空；负载乘 100 存整数。
CREATE TABLE server_metric (
    server_id      BIGINT           NOT NULL REFERENCES server (id) ON DELETE CASCADE,
    bucket_at      TIMESTAMPTZ      NOT NULL,          -- 5 分钟时段的起点
    cpu            DOUBLE PRECISION,                   -- CPU 占用百分比；面板没报为空
    online_clients INTEGER          NOT NULL DEFAULT 0,
    mem_used       BIGINT,                             -- 内存已用 / 总量（字节）
    mem_total      BIGINT,
    disk_used      BIGINT,                             -- 磁盘已用 / 总量（字节）
    disk_total     BIGINT,
    load1_x100     INTEGER,                            -- 1 分钟负载 × 100
    net_up_rate    BIGINT,                             -- 上行 / 下行速率（字节每秒）
    net_down_rate  BIGINT,
    tcp_count      INTEGER,                            -- TCP / UDP 连接数
    udp_count      INTEGER,
    PRIMARY KEY (server_id, bucket_at)
);
CREATE INDEX idx_server_metric_bucket_at ON server_metric (bucket_at);

-- 服务器网卡流量按天累计（docs/03 §5.1 账单与流量额度，Ruling-84）：day 是站点时区的日期，
-- sent / recv 是当天各次探测算出的增量之和；保留 62 天。
CREATE TABLE server_nic_traffic_daily (
    server_id BIGINT NOT NULL REFERENCES server (id) ON DELETE CASCADE,
    day       DATE   NOT NULL,
    sent      BIGINT NOT NULL DEFAULT 0,
    recv      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (server_id, day)
);
CREATE INDEX idx_server_nic_traffic_daily_day ON server_nic_traffic_daily (day);

-- 三网延迟（docs/03 §5.20 三网延迟检测，Ruling-86）：一次检测每家运营商一行，保留 7 天。
-- avg_ms 是平均延迟（毫秒，四舍五入），全丢为空；loss_percent 是丢包率。
CREATE TABLE server_latency (
    server_id    BIGINT      NOT NULL REFERENCES server (id) ON DELETE CASCADE,
    isp          VARCHAR(8)  NOT NULL,                 -- telecom / unicom / mobile
    sampled_at   TIMESTAMPTZ NOT NULL,
    avg_ms       INTEGER,
    loss_percent SMALLINT    NOT NULL DEFAULT 0,
    probe_city   VARCHAR(64) NOT NULL DEFAULT '',
    PRIMARY KEY (server_id, isp, sampled_at),
    CONSTRAINT ck_server_latency_isp CHECK (isp IN ('telecom', 'unicom', 'mobile')),
    CONSTRAINT ck_server_latency_loss CHECK (loss_percent BETWEEN 0 AND 100)
);
CREATE INDEX idx_server_latency_sampled_at ON server_latency (sampled_at);

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
    traffic_reset      VARCHAR(8)   NOT NULL DEFAULT 'never', -- 周期流量重置：never / hourly / daily / weekly / monthly
    traffic_reset_day  INTEGER      NOT NULL DEFAULT 1,       -- 按月重置的日期（1–31）
    allow_client_chain BOOLEAN      NOT NULL DEFAULT FALSE,   -- 功能开关：客户端链式（M6）
    allow_custom_node  BOOLEAN      NOT NULL DEFAULT FALSE,   -- 功能开关：自有落地节点（M6）
    allow_server_chain BOOLEAN      NOT NULL DEFAULT FALSE,   -- 功能开关：服务端链式（M7）
    remark             VARCHAR(255) NOT NULL DEFAULT '',
    is_on_sale         BOOLEAN      NOT NULL DEFAULT FALSE,   -- 是否在商店上架（docs/03 §5.12）
    is_trial           BOOLEAN      NOT NULL DEFAULT FALSE,   -- 试用套餐：每个用户终身只能下单一次
    sort_order         INTEGER      NOT NULL DEFAULT 0,
    description        VARCHAR(500) NOT NULL DEFAULT '',      -- 卖点说明，纯文本，一行一条
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
    renew_every_days      INTEGER      NOT NULL DEFAULT 0,   -- 免费续期（U4）：只由管理员在用户上设置（Ruling-20）
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
    client_cred_enc       BYTEA        NOT NULL,             -- 面板凭据（UUID、密码）AES-256-GCM，关联数据 end_user:<id>:client
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
    balance               BIGINT       NOT NULL DEFAULT 0,   -- 余额（分），docs/03 §5.14
    auto_renew            BOOLEAN      NOT NULL DEFAULT FALSE, -- 扣余额自动续费（docs/03 §5.16）
    plan_price_id         BIGINT,                            -- 上次购买的时长选项；外键在 plan_price 建表后补
    expire_notified_for   TIMESTAMPTZ,                       -- 已发过到期提醒的那个到期时间
    traffic_notified      BOOLEAN      NOT NULL DEFAULT FALSE, -- 本轮额度是否已发过流量提醒
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    agent_id              BIGINT       REFERENCES admin (id), -- 所属代理（Ruling-102）；NULL = 平台用户。管理员不物理删除，不带级联
    CONSTRAINT uk_end_user_username UNIQUE (username),
    CONSTRAINT ck_end_user_email_lower CHECK (email IS NULL OR email = lower(email)),
    CONSTRAINT ck_end_user_balance CHECK (balance BETWEEN 0 AND 9999999999)
);
CREATE UNIQUE INDEX uk_end_user_email ON end_user (email) WHERE email IS NOT NULL;
CREATE INDEX idx_end_user_invited_by ON end_user (invited_by) WHERE invited_by IS NOT NULL;
-- 代理侧的列表、计数都带“未删除”并按创建时间倒序翻页，索引照这个形状建（Ruling-102）
CREATE INDEX idx_end_user_agent_id ON end_user (agent_id, created_at DESC, id DESC) WHERE agent_id IS NOT NULL AND deleted_at IS NULL;
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
    links_stamp     VARCHAR(32) NOT NULL DEFAULT '',          -- 取链接时该面板的入站 / 对外地址摘要，与当前不同即重取（Ruling-92）
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
-- 总览分析按时间范围扫这张表（docs/03 §5.20 总览分析，Ruling-66）
CREATE INDEX idx_user_traffic_hourly_hour ON user_traffic_hourly (hour);

-- 按服务器的小时流量（Ruling-66）：用量任务算出每个 client 的增量时顺带累加；只有上线之后的数据。
CREATE TABLE server_traffic_hourly (
    server_id BIGINT      NOT NULL REFERENCES server (id) ON DELETE CASCADE,
    hour      TIMESTAMPTZ NOT NULL,
    up        BIGINT      NOT NULL DEFAULT 0,
    down      BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (server_id, hour)
);
CREATE INDEX idx_server_traffic_hourly_hour ON server_traffic_hourly (hour);

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
    impersonator_admin_id BIGINT REFERENCES admin (id) ON DELETE CASCADE, -- 非空 = 管理员代入的只读会话（docs/03 §5.17）
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

-- 邀请码（docs/03 §5.8）：owner_user_id 非空 = 用户的个人码（每人一行，重置时原地换码，已用次数不清零）；
-- agent_id 非空 = 代理的码（docs/03 §5.23，Ruling-102），两者互斥；都为空 = 管理员发的码。
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
    agent_id      BIGINT       REFERENCES admin (id),  -- 代理的码：经它注册的用户归该代理
    CONSTRAINT uk_invite_code_code UNIQUE (code),
    CONSTRAINT ck_invite_code_single_owner CHECK (owner_user_id IS NULL OR agent_id IS NULL)
);
CREATE UNIQUE INDEX uk_invite_code_owner ON invite_code (owner_user_id) WHERE owner_user_id IS NOT NULL;
CREATE INDEX idx_invite_code_agent_id ON invite_code (agent_id) WHERE agent_id IS NOT NULL;

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

-- Telegram 发件箱（docs/03 §5.20，Ruling-42）：纯文本，发出或放弃后清空正文；记录保留 30 天
CREATE TABLE telegram_outbox (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id         VARCHAR(64)  NOT NULL,
    text            TEXT,                              -- NULL = 已清空
    status          VARCHAR(16)  NOT NULL DEFAULT 'pending',
    attempts        INTEGER      NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_error      VARCHAR(255) NOT NULL DEFAULT '',  -- 脱敏后的错误，不含 token
    sent_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT ck_telegram_outbox_status CHECK (status IN ('pending', 'sent', 'failed'))
);
CREATE INDEX idx_telegram_outbox_queue ON telegram_outbox (next_attempt_at) WHERE status = 'pending';
CREATE INDEX idx_telegram_outbox_created_at ON telegram_outbox (created_at);

-- 后台任务的少量持久状态（如日报已发到哪一天），一行一个键
CREATE TABLE job_state (
    key        VARCHAR(64)  PRIMARY KEY,
    value      VARCHAR(255) NOT NULL,
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

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

-- 共享账号（docs/03 §5.9）：密码 AES-GCM，关联数据 shared_account:<id>。
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

-- M5：商店与钱包（docs/03 §5.12–§5.17）---------------------------------------------------

-- 时长选项（docs/03 §5.12，Ruling-29）：套餐的可售规格 = 天数 + 价格（整数分）；同一套餐天数不重复，最多 6 个由服务端校验。
CREATE TABLE plan_price (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    plan_id       BIGINT      NOT NULL REFERENCES plan (id) ON DELETE CASCADE,
    duration_days INTEGER     NOT NULL,
    price         BIGINT      NOT NULL,                -- 分
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_plan_price_plan_days UNIQUE (plan_id, duration_days),
    CONSTRAINT ck_plan_price_days CHECK (duration_days BETWEEN 1 AND 3650),
    CONSTRAINT ck_plan_price_price CHECK (price BETWEEN 0 AND 99999999)
);

-- 流量包（docs/03 §5.12）：只加额度不改到期。
CREATE TABLE traffic_pack (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          VARCHAR(64) NOT NULL,
    traffic_bytes BIGINT      NOT NULL,
    price         BIGINT      NOT NULL,                -- 分
    is_on_sale    BOOLEAN     NOT NULL DEFAULT FALSE,
    sort_order    INTEGER     NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_traffic_pack_name UNIQUE (name),
    CONSTRAINT ck_traffic_pack_bytes CHECK (traffic_bytes > 0),
    CONSTRAINT ck_traffic_pack_price CHECK (price BETWEEN 0 AND 99999999)
);

-- 优惠码（docs/03 §5.15）：码存大写；次数判断与占用在下单事务内完成（行加锁）。
CREATE TABLE coupon (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code              VARCHAR(32)  NOT NULL,
    kind              VARCHAR(8)   NOT NULL,           -- percent / fixed
    value             BIGINT       NOT NULL,           -- percent：1–100；fixed：分
    all_plans         BOOLEAN      NOT NULL DEFAULT TRUE,  -- 假 = 只适用于 coupon_plan 里的套餐
    for_traffic_pack  BOOLEAN      NOT NULL DEFAULT FALSE, -- 是否适用于流量包
    min_amount        BIGINT       NOT NULL DEFAULT 0,     -- 最低消费（分）
    max_uses          INTEGER      NOT NULL DEFAULT 0,     -- 总次数上限，0 不限
    max_uses_per_user INTEGER      NOT NULL DEFAULT 1,     -- 每人次数上限，0 不限
    used_count        INTEGER      NOT NULL DEFAULT 0,
    starts_at         TIMESTAMPTZ,                         -- NULL = 立即生效
    ends_at           TIMESTAMPTZ,                         -- NULL = 不过期
    is_disabled       BOOLEAN      NOT NULL DEFAULT FALSE,
    remark            VARCHAR(255) NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_coupon_code UNIQUE (code),
    CONSTRAINT ck_coupon_kind CHECK (kind IN ('percent', 'fixed')),
    CONSTRAINT ck_coupon_value CHECK (value >= 1 AND (kind <> 'percent' OR value <= 100) AND value <= 99999999)
);

CREATE TABLE coupon_plan (
    coupon_id BIGINT NOT NULL REFERENCES coupon (id) ON DELETE CASCADE,
    plan_id   BIGINT NOT NULL REFERENCES plan (id) ON DELETE CASCADE,
    PRIMARY KEY (coupon_id, plan_id)
);

-- 订单（docs/03 §5.13，Ruling-28）：只有“已支付”一种状态。用户删除后保留，user_id 置空、username 留快照（Ruling-32）。
CREATE TABLE shop_order (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_no        VARCHAR(24)  NOT NULL,             -- O + yyyyMMddHHmmss + 6 位随机数字（docs/03 §6）
    user_id         BIGINT       REFERENCES end_user (id) ON DELETE SET NULL,
    username        VARCHAR(64)  NOT NULL,             -- 下单时的用户名
    kind            VARCHAR(16)  NOT NULL,             -- plan / traffic_pack / renew
    plan_id         BIGINT       REFERENCES plan (id) ON DELETE SET NULL,
    traffic_pack_id BIGINT       REFERENCES traffic_pack (id) ON DELETE SET NULL,
    item            JSONB        NOT NULL,             -- 商品快照：名称、天数、流量、单价
    is_trial        BOOLEAN      NOT NULL DEFAULT FALSE,
    amount_original BIGINT       NOT NULL,             -- 原价（分）
    amount_discount BIGINT       NOT NULL DEFAULT 0,   -- 优惠（分）
    amount_paid     BIGINT       NOT NULL,             -- 实付（分）= 原价 − 优惠
    coupon_id       BIGINT       REFERENCES coupon (id) ON DELETE SET NULL,
    coupon_code     VARCHAR(32)  NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_shop_order_no UNIQUE (order_no),
    CONSTRAINT ck_shop_order_kind CHECK (kind IN ('plan', 'traffic_pack', 'renew')),
    CONSTRAINT ck_shop_order_amounts CHECK (amount_original >= 0 AND amount_discount >= 0 AND amount_paid = amount_original - amount_discount AND amount_paid >= 0)
);
CREATE INDEX idx_shop_order_user ON shop_order (user_id, id DESC);
CREATE INDEX idx_shop_order_created_at ON shop_order (created_at);
-- 试用套餐每个用户终身只能下单一次（docs/03 §5.12）
CREATE UNIQUE INDEX uk_shop_order_trial ON shop_order (user_id) WHERE is_trial;

-- 优惠码使用记录：每人次数上限按它统计。
CREATE TABLE coupon_usage (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    coupon_id  BIGINT      NOT NULL REFERENCES coupon (id) ON DELETE CASCADE,
    user_id    BIGINT      REFERENCES end_user (id) ON DELETE SET NULL,
    order_id   BIGINT      NOT NULL REFERENCES shop_order (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_coupon_usage_order UNIQUE (order_id)
);
CREATE INDEX idx_coupon_usage_coupon_user ON coupon_usage (coupon_id, user_id);

-- 充值码（docs/03 §5.15，Ruling-31）：只存 HMAC（查找）与 AES-GCM 密文（关联数据 topup_code:<aad_key>），不存明文。
CREATE TABLE topup_code (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code_hash        BYTEA        NOT NULL,
    code_enc         BYTEA        NOT NULL,
    aad_key          VARCHAR(32)  NOT NULL,            -- 随机值，只作加密关联数据
    code_tail        VARCHAR(4)   NOT NULL,            -- 末 4 位，列表里显示
    amount           BIGINT       NOT NULL,            -- 面值（分）
    batch            VARCHAR(64)  NOT NULL DEFAULT '', -- 批次备注
    expires_at       TIMESTAMPTZ,                      -- NULL = 不过期
    is_disabled      BOOLEAN      NOT NULL DEFAULT FALSE,
    used_by          BIGINT       REFERENCES end_user (id) ON DELETE SET NULL,
    used_by_username VARCHAR(64)  NOT NULL DEFAULT '',
    used_at          TIMESTAMPTZ,                      -- 非空 = 已使用
    created_by       BIGINT       REFERENCES admin (id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_topup_code_hash UNIQUE (code_hash),
    CONSTRAINT ck_topup_code_amount CHECK (amount BETWEEN 1 AND 99999999)
);
CREATE INDEX idx_topup_code_created_at ON topup_code (created_at DESC);

-- 充值单（docs/03 §5.15）：经支付网关给余额充值；(gateway, gateway_trade_no) 唯一，重复回调只入账一次。
CREATE TABLE payment (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    trade_no         VARCHAR(24)  NOT NULL,            -- P + yyyyMMddHHmmss + 6 位随机数字
    user_id          BIGINT       REFERENCES end_user (id) ON DELETE SET NULL,
    username         VARCHAR(64)  NOT NULL,
    gateway          VARCHAR(32)  NOT NULL,
    amount           BIGINT       NOT NULL,            -- 分
    status           VARCHAR(16)  NOT NULL DEFAULT 'pending', -- pending / paid / expired
    gateway_trade_no VARCHAR(128),
    expires_at       TIMESTAMPTZ  NOT NULL,            -- 未支付的过期时间
    paid_at          TIMESTAMPTZ,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_payment_trade_no UNIQUE (trade_no),
    CONSTRAINT ck_payment_status CHECK (status IN ('pending', 'paid', 'expired')),
    CONSTRAINT ck_payment_amount CHECK (amount BETWEEN 1 AND 99999999)
);
CREATE UNIQUE INDEX uk_payment_gateway_trade ON payment (gateway, gateway_trade_no) WHERE gateway_trade_no IS NOT NULL;
CREATE INDEX idx_payment_user ON payment (user_id, id DESC);

-- 返利（docs/03 §5.16，Ruling-34）：一笔订单最多一条。
CREATE TABLE rebate (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    inviter_id       BIGINT      REFERENCES end_user (id) ON DELETE SET NULL,
    inviter_username VARCHAR(64) NOT NULL,
    invitee_id       BIGINT      REFERENCES end_user (id) ON DELETE SET NULL,
    invitee_username VARCHAR(64) NOT NULL,
    order_id         BIGINT      NOT NULL REFERENCES shop_order (id) ON DELETE CASCADE,
    order_amount     BIGINT      NOT NULL,             -- 订单实付（分）
    percent          INTEGER     NOT NULL,             -- 当时的返利比例
    amount           BIGINT      NOT NULL,             -- 返利（分）
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_rebate_order UNIQUE (order_id),
    CONSTRAINT ck_rebate_amount CHECK (amount > 0)
);
CREATE INDEX idx_rebate_inviter ON rebate (inviter_id, id DESC);
CREATE INDEX idx_rebate_invitee ON rebate (invitee_id);

-- 余额流水（docs/03 §5.14）：每次余额变动一条，与改余额同一事务。用户删除后保留（Ruling-32）。
CREATE TABLE wallet_ledger (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       BIGINT       REFERENCES end_user (id) ON DELETE SET NULL,
    username      VARCHAR(64)  NOT NULL,
    kind          VARCHAR(16)  NOT NULL,               -- topup_code / topup_online / purchase / renew / rebate / admin_adjust / forfeit
    amount        BIGINT       NOT NULL,               -- 带符号（分）
    balance_after BIGINT       NOT NULL,
    ref_type      VARCHAR(16)  NOT NULL DEFAULT '',    -- order / topup_code / payment / rebate；没有关联对象为空串
    ref_id        BIGINT,
    remark        VARCHAR(255) NOT NULL DEFAULT '',
    admin_id      BIGINT       REFERENCES admin (id) ON DELETE SET NULL, -- 管理员调整时的操作人
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT ck_wallet_ledger_kind CHECK (kind IN ('topup_code', 'topup_online', 'purchase', 'renew', 'rebate', 'admin_adjust', 'forfeit')),
    CONSTRAINT ck_wallet_ledger_amount CHECK (amount <> 0 AND balance_after >= 0)
);
CREATE INDEX idx_wallet_ledger_user ON wallet_ledger (user_id, id DESC);
CREATE INDEX idx_wallet_ledger_created_at ON wallet_ledger (created_at);

-- 切换为用户身份的一次性票据（docs/03 §5.17）：管理端签发，客户门户兑换成代入会话；只存 SHA-256。
CREATE TABLE impersonate_ticket (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ticket_hash BYTEA       NOT NULL,
    admin_id    BIGINT      NOT NULL REFERENCES admin (id) ON DELETE CASCADE,
    user_id     BIGINT      NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_impersonate_ticket_hash UNIQUE (ticket_hash)
);
ALTER TABLE end_user ADD CONSTRAINT end_user_plan_price_id_fkey FOREIGN KEY (plan_price_id) REFERENCES plan_price (id) ON DELETE SET NULL;

-- 链式出口（docs/03 §5.21，Ruling-47）：只用作链路落地的外部节点。user_id 为空 = 平台出口（管理员添加，经 node_group_exit 授权），
-- 否则 = 用户出口（M6 的自有落地节点）。链接含凭据，AES-256-GCM 加密，关联数据 custom_node:<id>:link（沿用 M6 的写法；
-- 先插入空值拿到 id 再回填）；protocol / address / port 是从链接解析出的展示用副本。
-- allow_server / allow_client 只对平台出口有意义；resolved_ip 是用户出口用于服务端链式时固定下来的地址（Ruling-48）。
CREATE TABLE chain_exit (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      BIGINT       REFERENCES end_user (id) ON DELETE CASCADE,
    name         VARCHAR(64)  NOT NULL,
    protocol     VARCHAR(16)  NOT NULL,
    address      VARCHAR(255) NOT NULL,
    port         INTEGER      NOT NULL,
    link_enc     BYTEA        NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    remark       VARCHAR(255) NOT NULL DEFAULT '',
    is_enabled   BOOLEAN      NOT NULL DEFAULT TRUE,     -- 用户出口由管理员停用 / 启用
    allow_server BOOLEAN      NOT NULL DEFAULT TRUE,
    allow_client BOOLEAN      NOT NULL DEFAULT TRUE,
    resolved_ip  INET,
    resolved_at  TIMESTAMPTZ,
    assign_mode  VARCHAR(8)   NOT NULL DEFAULT 'group',  -- 平台出口的分配方式：group 按节点组 / user 指定用户（Ruling-64）
    CONSTRAINT uk_chain_exit_user_name UNIQUE (user_id, name),
    CONSTRAINT ck_chain_exit_assign_mode CHECK (assign_mode IN ('group', 'user')),
    CONSTRAINT ck_chain_exit_protocol CHECK (protocol IN ('vless', 'vmess', 'trojan', 'shadowsocks', 'socks', 'http')),
    CONSTRAINT ck_chain_exit_port CHECK (port BETWEEN 1 AND 65535)
);
CREATE UNIQUE INDEX uk_chain_exit_platform_name ON chain_exit (name) WHERE user_id IS NULL;

-- 平台出口加入节点组，沿用“套餐 → 节点组”授权（Ruling-47）
CREATE TABLE node_group_exit (
    node_group_id BIGINT NOT NULL REFERENCES node_group (id) ON DELETE CASCADE,
    exit_id       BIGINT NOT NULL REFERENCES chain_exit (id) ON DELETE CASCADE,
    PRIMARY KEY (node_group_id, exit_id)
);
CREATE INDEX idx_node_group_exit_exit_id ON node_group_exit (exit_id);

-- 平台出口指定给哪些用户（assign_mode = user 时生效，Ruling-64）
CREATE TABLE chain_exit_user (
    exit_id BIGINT NOT NULL REFERENCES chain_exit (id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    PRIMARY KEY (exit_id, user_id)
);
CREATE INDEX idx_chain_exit_user_user_id ON chain_exit_user (user_id);

-- 链式规则（docs/03 §5.18、§5.19，Ruling-46）：一组起飞节点（chain_route_entry）→ 一个落地（平台节点或出口，恰好一个）。
-- kind = client 只在订阅里表达；server 是服务端链式。position 在同一用户同一类型内越小越优先（可有空号，调顺序在事务里整体重排）。
-- 状态是计算值，不落库；quarantine_hash 是失败隔离时记下的内容指纹，空 = 未隔离（Ruling-49）。
-- 落地节点或出口被删除时规则随之删除；每个用户的条数上限由服务端校验。
CREATE TABLE chain_route (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         BIGINT      NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    kind            VARCHAR(16) NOT NULL,
    position        INTEGER     NOT NULL,
    name            VARCHAR(64) NOT NULL DEFAULT '',          -- 空 = 显示落地的名称
    exit_node_id    BIGINT      REFERENCES node (id) ON DELETE CASCADE,
    exit_id         BIGINT      REFERENCES chain_exit (id) ON DELETE CASCADE,
    is_enabled      BOOLEAN     NOT NULL DEFAULT TRUE,
    quarantine_hash VARCHAR(64) NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_chain_route_kind CHECK (kind IN ('client', 'server')),
    CONSTRAINT ck_chain_route_exit CHECK (num_nonnulls(exit_node_id, exit_id) = 1),
    CONSTRAINT ck_chain_route_position CHECK (position >= 1),
    CONSTRAINT uk_chain_route_position UNIQUE (user_id, kind, position) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX idx_chain_route_exit_node ON chain_route (exit_node_id) WHERE exit_node_id IS NOT NULL;
CREATE INDEX idx_chain_route_exit ON chain_route (exit_id) WHERE exit_id IS NOT NULL;

CREATE TABLE chain_route_entry (
    route_id      BIGINT NOT NULL REFERENCES chain_route (id) ON DELETE CASCADE,
    entry_node_id BIGINT NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    PRIMARY KEY (route_id, entry_node_id)
);
CREATE INDEX idx_chain_route_entry_node ON chain_route_entry (entry_node_id);

-- 起飞节点被删光的规则一并删除（docs/03 §5.18 阻断与删除）；节点的删除入口不止一处，放在库里保证不漏
CREATE FUNCTION chain_route_drop_empty() RETURNS trigger AS $$
BEGIN
    DELETE FROM chain_route r
    WHERE r.id = OLD.route_id
      AND NOT EXISTS (SELECT 1 FROM chain_route_entry e WHERE e.route_id = r.id);
    RETURN NULL;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER trg_chain_route_entry_drop_empty
    AFTER DELETE ON chain_route_entry
    FOR EACH ROW EXECUTE FUNCTION chain_route_drop_empty();

-- 链路下发记录（docs/03 §5.19，Ruling-40）：每台起飞服务器一行，记录面板上平台条目的下发状态。
-- desired_hash 是期望平台条目的 SHA-256；fail_count 只对同一 desired_hash 累计，期望变化即清零；
-- backup_enc 是最近一次写入前的整份 Xray 模板，AES-256-GCM 加密，关联数据 chain_apply:<server_id>:backup。
CREATE TABLE chain_apply (
    server_id        BIGINT      PRIMARY KEY REFERENCES server (id) ON DELETE CASCADE,
    status           VARCHAR(16) NOT NULL DEFAULT 'pending',
    desired_hash     VARCHAR(64) NOT NULL DEFAULT '',
    applied_rule_ids BIGINT[]    NOT NULL DEFAULT '{}',   -- 当前面板上生效的服务端规则（chain_route）id
    fail_count       INTEGER     NOT NULL DEFAULT 0,
    last_error       TEXT        NOT NULL DEFAULT '',     -- 脱敏后的最近错误
    last_attempt_at  TIMESTAMPTZ,
    next_attempt_at  TIMESTAMPTZ,                         -- 失败退避到这个时刻；空 = 不限
    applied_at       TIMESTAMPTZ,
    backup_enc       BYTEA,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_chain_apply_status CHECK (status IN ('pending', 'applied', 'failed'))
);

-- 大陆连通检测（docs/03 §5.20，Ruling-67）：每个去重后的检测目标（地址，端口，传输）一行，记最近一次结论。
-- 目标由后台任务按节点目录同步，不再对应任何节点时删除；probes 是各探测点的结果（国家、城市、网络、是否通、延迟）。
-- blocked_streak 是连续判为 blocked 的轮数，达到 2 才告警；alert_sent_at 非空 = 这次被墙已发过告警。
CREATE TABLE node_reach (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    address        VARCHAR(255) NOT NULL,
    port           INTEGER      NOT NULL,
    transport      VARCHAR(4)   NOT NULL,                   -- tcp：测端口；udp：只测 ICMP
    verdict        VARCHAR(16)  NOT NULL DEFAULT 'pending', -- pending / ok / partial / blocked / port_down / unknown
    cn_ok          INTEGER      NOT NULL DEFAULT 0,
    cn_total       INTEGER      NOT NULL DEFAULT 0,
    control_ok     INTEGER      NOT NULL DEFAULT 0,
    control_total  INTEGER      NOT NULL DEFAULT 0,
    ip_reachable   BOOLEAN,                                 -- tcp 目标 blocked 时补测的大陆 ICMP 是否还通；其余为空
    probes         JSONB        NOT NULL DEFAULT '[]',
    last_error     VARCHAR(255) NOT NULL DEFAULT '',
    blocked_streak INTEGER      NOT NULL DEFAULT 0,
    blocked_since  TIMESTAMPTZ,
    alert_sent_at  TIMESTAMPTZ,
    checked_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_node_reach_target UNIQUE (address, port, transport),
    CONSTRAINT ck_node_reach_transport CHECK (transport IN ('tcp', 'udp')),
    CONSTRAINT ck_node_reach_verdict CHECK (verdict IN ('pending', 'ok', 'partial', 'blocked', 'port_down', 'unknown')),
    CONSTRAINT ck_node_reach_port CHECK (port BETWEEN 1 AND 65535)
);

-- SSH 凭据（docs/03 §5.22，Ruling-68）：一份凭据可被多台主机引用，被引用时不能删。
-- secret_enc 是私钥（OpenSSH / PEM）或密码，passphrase_enc 是私钥口令；AES-256-GCM，
-- 关联数据 ssh_credential:<id>:secret / ssh_credential:<id>:passphrase。key_fingerprint 是公钥指纹，只用于展示。
CREATE TABLE ssh_credential (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name            VARCHAR(64)  NOT NULL,
    username        VARCHAR(64)  NOT NULL,
    auth_type       VARCHAR(8)   NOT NULL,                 -- key / password
    secret_enc      BYTEA        NOT NULL,
    passphrase_enc  BYTEA,
    key_fingerprint VARCHAR(64)  NOT NULL DEFAULT '',      -- SHA256:<Base64>；密码认证为空串
    remark          VARCHAR(255) NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_ssh_credential_name UNIQUE (name),
    CONSTRAINT ck_ssh_credential_auth CHECK (auth_type IN ('key', 'password'))
);

-- DNS 账号（docs/03 §5.22，Ruling-72）：DNS 服务商的 API 令牌，一份对应一个根域名；给主机建解析与做证书的 DNS 验证。
-- token_enc 用 AES-256-GCM 加密，关联数据 dns_account:<id>:token；签发证书时经 SSH 临时传给主机、不在主机上落盘（Ruling-74）。被主机引用时不能删。
CREATE TABLE dns_account (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          VARCHAR(64)  NOT NULL,
    provider_type VARCHAR(16)  NOT NULL,                   -- 服务商：cloudflare
    zone          VARCHAR(255) NOT NULL,                   -- 根域名，小写，如 example.com
    token_enc     BYTEA        NOT NULL,
    remark        VARCHAR(255) NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_dns_account_name UNIQUE (name)
);

-- 托管主机（docs/03 §5.22）：一台能 SSH 登录的机器；装好面板并接入后 server_id 指向对应的服务器。
-- host_key_sha256 是首次连接时记录并固定的主机公钥指纹，空串 = 还没记录；os_id、arch 取自最近一次检测。
-- 删服务器时主机保留、解除关联；删主机时任务历史一并删除。
-- dns_account_id 与 domain 同有同无：有则面板证书由主机上的 acme.sh 申请（Ruling-74），cert_not_after 是这张证书的到期时间，空 = 还没申请到；
-- cert_renew_at 是下次续期时间：成功时取 acme.sh 给出的，没成功时记为当时（下一轮重试）；空 = 不自动申请。
CREATE TABLE server_host (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name            VARCHAR(64)  NOT NULL,                 -- 接入时用作服务器名
    address         VARCHAR(255) NOT NULL,                 -- IP 或域名
    ssh_port        INTEGER      NOT NULL DEFAULT 22,
    credential_id   BIGINT       NOT NULL REFERENCES ssh_credential (id) ON DELETE RESTRICT,
    provider_type   VARCHAR(16)  NOT NULL,                 -- 面板类型：xui
    region          VARCHAR(2)   NOT NULL DEFAULT '',
    panel_port      INTEGER,                               -- 安装时指定的面板端口；空 = 随机
    host_key_sha256 VARCHAR(64)  NOT NULL DEFAULT '',
    os_id           VARCHAR(32)  NOT NULL DEFAULT '',
    arch            VARCHAR(16)  NOT NULL DEFAULT '',
    server_id       BIGINT       REFERENCES server (id) ON DELETE SET NULL,
    dns_account_id  BIGINT       REFERENCES dns_account (id) ON DELETE RESTRICT,
    domain          VARCHAR(255) NOT NULL DEFAULT '',      -- 面板用的域名，小写；空串 = 不申请公开证书
    cert_not_after  TIMESTAMPTZ,
    cert_renew_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_server_host_name UNIQUE (name),
    CONSTRAINT uk_server_host_target UNIQUE (address, ssh_port),
    CONSTRAINT uk_server_host_server UNIQUE (server_id),
    CONSTRAINT ck_server_host_region CHECK (region ~ '^([A-Z]{2})?$'),
    CONSTRAINT ck_server_host_ssh_port CHECK (ssh_port BETWEEN 1 AND 65535),
    CONSTRAINT ck_server_host_panel_port CHECK (panel_port IS NULL OR panel_port BETWEEN 1 AND 65535),
    CONSTRAINT ck_server_host_domain CHECK ((dns_account_id IS NULL) = (domain = ''))
);
CREATE INDEX idx_server_host_credential ON server_host (credential_id);
CREATE INDEX idx_server_host_dns_account ON server_host (dns_account_id);
CREATE UNIQUE INDEX uk_server_host_domain ON server_host (domain) WHERE domain <> '';

-- 主机任务（docs/03 §5.22，Ruling-71）：经 SSH 做的每件事一行。一台主机同时只有一条未结束的任务（部分唯一索引）。
-- log 是脱敏后的步骤日志，上限 64 KB；error_code 是失败原因大类，last_error 是脱敏后的摘要。
CREATE TABLE host_task (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id     BIGINT       NOT NULL REFERENCES server_host (id) ON DELETE CASCADE,
    kind        VARCHAR(16)  NOT NULL,
    status      VARCHAR(16)  NOT NULL DEFAULT 'queued',
    step        VARCHAR(16)  NOT NULL DEFAULT '',
    error_code  VARCHAR(32)  NOT NULL DEFAULT '',
    last_error  VARCHAR(500) NOT NULL DEFAULT '',
    log         TEXT         NOT NULL DEFAULT '',
    payload     JSONB        NOT NULL DEFAULT '{}',         -- 任务参数（deploy_inbounds 的预设、端口与节点组，Ruling-76）
    created_by  BIGINT       REFERENCES admin (id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    CONSTRAINT ck_host_task_kind CHECK (kind IN ('provision', 'verify', 'restart_panel', 'restart_core', 'upgrade', 'reissue_token', 'enable_tls', 'diagnose', 'deploy_inbounds')),
    CONSTRAINT ck_host_task_status CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled'))
);
CREATE UNIQUE INDEX uk_host_task_active ON host_task (host_id) WHERE status IN ('queued', 'running');
CREATE INDEX idx_host_task_host ON host_task (host_id, id DESC);
CREATE INDEX idx_host_task_queued ON host_task (id) WHERE status = 'queued';

-- 二级代理（docs/03 §5.23，Ruling-101 – Ruling-104）----------------------------------
-- 代理是 admin.kind = agent 的账号；名下用户见 end_user.agent_id，代理的邀请码见 invite_code.agent_id。

-- 代理配额（Ruling-103）：每个代理一行，与 admin 同时创建。流量池是累计制，已用量只增不减（删除用户、到期都不退回）。
CREATE TABLE agent_profile (
    admin_id           BIGINT       PRIMARY KEY REFERENCES admin (id),
    max_users          INTEGER      NOT NULL DEFAULT 0,  -- 名下未删除用户数上限
    traffic_pool_total BIGINT       NOT NULL DEFAULT 0,  -- 流量池总量（字节），不能调到低于已用量
    traffic_pool_used  BIGINT       NOT NULL DEFAULT 0,  -- 已扣减（字节），与开通在同一事务里累加
    remark             VARCHAR(255) NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT ck_agent_profile_max_users CHECK (max_users >= 0),
    CONSTRAINT ck_agent_profile_pool CHECK (traffic_pool_total >= 0 AND traffic_pool_used >= 0 AND traffic_pool_used <= traffic_pool_total)
);

-- 代理可分配的套餐白名单（Ruling-103）：只收流量与时长都有限的套餐，由 hub-server 校验；套餐删除时一并删除。
CREATE TABLE agent_plan (
    admin_id BIGINT NOT NULL REFERENCES agent_profile (admin_id) ON DELETE CASCADE,
    plan_id  BIGINT NOT NULL REFERENCES plan (id) ON DELETE CASCADE,
    PRIMARY KEY (admin_id, plan_id)
);
CREATE INDEX idx_agent_plan_plan_id ON agent_plan (plan_id);

-- 开通记录（Ruling-103）：代理每次扣减流量池写一行，只追加，线下结算的依据。用户或套餐删除后靠快照列保留。
CREATE TABLE agent_allocation (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id      BIGINT       NOT NULL REFERENCES admin (id),
    user_id       BIGINT       REFERENCES end_user (id) ON DELETE SET NULL,
    username      VARCHAR(64)  NOT NULL,                 -- 开通时的用户名
    plan_id       BIGINT       REFERENCES plan (id) ON DELETE SET NULL,
    plan_name     VARCHAR(64)  NOT NULL,                 -- 开通时的套餐名
    traffic_bytes BIGINT       NOT NULL,                 -- 本次扣减的流量（字节）= 套餐流量 × 时长内的重置周期数
    duration_days INTEGER      NOT NULL,                 -- 套餐时长（天）
    kind          VARCHAR(8)   NOT NULL,                 -- create 新建用户 / assign 分配或续期
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT ck_agent_allocation_kind CHECK (kind IN ('create', 'assign')),
    CONSTRAINT ck_agent_allocation_amounts CHECK (traffic_bytes > 0 AND duration_days > 0)
);
CREATE INDEX idx_agent_allocation_agent ON agent_allocation (agent_id, created_at DESC);
-- 用户 / 套餐被物理删除时要按这两列找引用行置空，没有索引就是整表顺扫
CREATE INDEX idx_agent_allocation_user ON agent_allocation (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX idx_agent_allocation_plan ON agent_allocation (plan_id) WHERE plan_id IS NOT NULL;

-- 种子数据 ---------------------------------------------------------------------
-- 内置角色 super_admin 隐式拥有全部权限码（docs/03 §4），不写 role_permission。
INSERT INTO role (code, name, is_builtin) VALUES ('super_admin', '超级管理员', TRUE);
-- 四个预设角色（Ruling-95）：权限由代码定义（partner = 全部权限码减超管专属），同样不写 role_permission。
INSERT INTO role (code, name, is_builtin) VALUES
    ('operator', '客服运营', TRUE),
    ('finance',  '财务',     TRUE),
    ('ops',      '运维',     TRUE),
    ('partner',  '合伙人',   TRUE);

-- 权限码清单与 docs/03 §4 权限码表逐行对应。admin:* 与 host:* 超管专属，不能放进其它角色（Ruling-95）。
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
    ('ticket:write',  '回复与关闭工单'),
    ('shop:read',     '查看商品定价、流量包与优惠码'),
    ('shop:write',    '管理商品定价、流量包与优惠码'),
    ('finance:read',  '查看订单、流水、充值单、返利与收支统计'),
    ('finance:write', '管理充值码、调整用户余额'),
    ('impersonate:write', '切换为用户身份'),
    ('host:read',     '查看 SSH 凭据、托管主机与任务'),
    ('host:write',    '管理 SSH 凭据与托管主机、发起部署与运维');
