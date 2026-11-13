-- 0007：M3 终端用户会话、两步验证、订阅与客户后台数据（docs/03 §5.3–§5.5）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。

-- 终端用户会话（docs/03 §5.3），结构与 admin_session 相同。
CREATE TABLE IF NOT EXISTS user_session (
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
CREATE INDEX IF NOT EXISTS idx_user_session_user_id ON user_session (user_id);
CREATE INDEX IF NOT EXISTS idx_user_session_expires_at ON user_session (expires_at);

ALTER TABLE end_user
    ADD COLUMN IF NOT EXISTS totp_secret_enc  BYTEA,                        -- AES-GCM；NULL = 未启用两步验证
    ADD COLUMN IF NOT EXISTS totp_pending_enc BYTEA,                        -- 开启流程中待确认的密钥
    ADD COLUMN IF NOT EXISTS totp_last_step   BIGINT  NOT NULL DEFAULT 0,   -- 最近一次成功使用的步长，防重放
    ADD COLUMN IF NOT EXISTS sub_token_hash   BYTEA,                        -- 订阅 token 的 SHA-256，用于查找（docs/03 §5.4）
    ADD COLUMN IF NOT EXISTS sub_token_enc    BYTEA,                        -- 订阅 token，AES-GCM，关联数据 end_user:<id>:sub
    ADD COLUMN IF NOT EXISTS cred_version     INTEGER NOT NULL DEFAULT 1,   -- 连接凭据版本，重置订阅时 +1
    ADD COLUMN IF NOT EXISTS last_used_at     TIMESTAMPTZ;                  -- 最近一次产生流量的时间
CREATE UNIQUE INDEX IF NOT EXISTS uk_end_user_sub_token_hash ON end_user (sub_token_hash) WHERE sub_token_hash IS NOT NULL;

ALTER TABLE provision
    ADD COLUMN IF NOT EXISTS cred_version    INTEGER NOT NULL DEFAULT 0,    -- 已写到面板的凭据版本；小于用户的即待更新
    ADD COLUMN IF NOT EXISTS links_enc       BYTEA,                         -- 面板生成的分享链接（JSON 数组），AES-GCM，关联数据 provision:<id>:links
    ADD COLUMN IF NOT EXISTS links_synced_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS is_online       BOOLEAN NOT NULL DEFAULT FALSE; -- 该 client 在面板上是否在线（流量任务顺带采集）

ALTER TABLE server
    ADD COLUMN IF NOT EXISTS online_clients INTEGER NOT NULL DEFAULT 0;     -- 面板在线 client 数（docs/03 §5.5）

-- 终端用户登录记录（docs/03 §5.3），保留 90 天。
CREATE TABLE IF NOT EXISTS user_login_log (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    client_ip  INET,
    is_success BOOLEAN     NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_user_login_log_user_created ON user_login_log (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_login_log_created_at ON user_login_log (created_at);

-- 订阅拉取记录（docs/03 §5.4），默认保留 30 天。
CREATE TABLE IF NOT EXISTS subscribe_log (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT       NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    format     VARCHAR(16)  NOT NULL,                  -- base64 / mihomo / singbox / xray
    client_ip  INET,
    user_agent VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_subscribe_log_user_created ON subscribe_log (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_subscribe_log_created_at ON subscribe_log (created_at);
