-- 0008：M4 自助运营——系统设置、邀请码、邮箱验证码、发件箱、公告、教程、共享账号、工单、签到（docs/03 §5.6–§5.11）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。

-- 系统设置（docs/03 §5.6）：键与类型由代码清单固定，库里没有的键取默认值；凭据不进这张表。
CREATE TABLE IF NOT EXISTS setting (
    key        VARCHAR(64) PRIMARY KEY,
    value      JSONB       NOT NULL,
    updated_by BIGINT      REFERENCES admin (id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 邀请码（docs/03 §5.8）：owner_user_id 非空 = 用户的个人码（每人一行，重置时原地换码，已用次数不清零）；空 = 管理员发的码。
CREATE TABLE IF NOT EXISTS invite_code (
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
CREATE UNIQUE INDEX IF NOT EXISTS uk_invite_code_owner ON invite_code (owner_user_id) WHERE owner_user_id IS NOT NULL;

-- 邮箱验证码（docs/03 §5.7）：只存 HMAC-SHA256；同一邮箱同一用途只有最新一条有效。
CREATE TABLE IF NOT EXISTS email_code (
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
CREATE INDEX IF NOT EXISTS idx_email_code_lookup ON email_code (email, purpose, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_email_code_created_at ON email_code (created_at);

-- 发件箱（docs/03 §5.7）：正文 AES-GCM（关联数据 mail_outbox:<aad_key>），发送成功或放弃后清空。
CREATE TABLE IF NOT EXISTS mail_outbox (
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
CREATE INDEX IF NOT EXISTS idx_mail_outbox_queue ON mail_outbox (priority, next_attempt_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_mail_outbox_created_at ON mail_outbox (created_at);

-- 公告（docs/03 §5.9）：正文为 Markdown 源文本（Ruling-25）。
CREATE TABLE IF NOT EXISTS announcement (
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
CREATE INDEX IF NOT EXISTS idx_announcement_published ON announcement (is_pinned DESC, published_at DESC) WHERE published_at IS NOT NULL;

-- 教程与下载（docs/03 §5.9）。
CREATE TABLE IF NOT EXISTS tutorial (
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
CREATE INDEX IF NOT EXISTS idx_tutorial_platform ON tutorial (platform, sort_order);

-- 共享账号（docs/03 §5.9）：口令 AES-GCM，关联数据 shared_account:<id>。
CREATE TABLE IF NOT EXISTS shared_account (
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
CREATE TABLE IF NOT EXISTS shared_account_plan (
    shared_account_id BIGINT NOT NULL REFERENCES shared_account (id) ON DELETE CASCADE,
    plan_id           BIGINT NOT NULL REFERENCES plan (id) ON DELETE CASCADE,
    PRIMARY KEY (shared_account_id, plan_id)
);

-- 工单（docs/03 §5.10）。
CREATE TABLE IF NOT EXISTS ticket (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       BIGINT       NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    subject       VARCHAR(100) NOT NULL,
    status        VARCHAR(16)  NOT NULL DEFAULT 'open', -- open / answered / closed
    last_reply_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    closed_at     TIMESTAMPTZ,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_user ON ticket (user_id, last_reply_at DESC);
CREATE INDEX IF NOT EXISTS idx_ticket_status ON ticket (status, last_reply_at DESC);

CREATE TABLE IF NOT EXISTS ticket_message (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ticket_id   BIGINT      NOT NULL REFERENCES ticket (id) ON DELETE CASCADE,
    author_kind VARCHAR(8)  NOT NULL,                  -- user / admin
    admin_id    BIGINT      REFERENCES admin (id) ON DELETE SET NULL, -- author_kind = admin 时的作者
    content     TEXT        NOT NULL,                  -- 纯文本
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ticket_message_ticket ON ticket_message (ticket_id, id);

-- 签到记录（docs/03 §5.11）：唯一约束保证每人每天一次（Ruling-26）。
CREATE TABLE IF NOT EXISTS checkin_log (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    checkin_date DATE        NOT NULL,                 -- 按设置 site.timezone 的自然日
    reward_bytes BIGINT      NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_checkin_log_user_date UNIQUE (user_id, checkin_date)
);

-- 终端用户的邮箱与邀请关系（docs/03 §5.8）。邮箱存小写，全站唯一；管理员建的用户可以没有邮箱。
ALTER TABLE end_user
    ADD COLUMN IF NOT EXISTS email             VARCHAR(254),
    ADD COLUMN IF NOT EXISTS email_verified_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS invited_by        BIGINT REFERENCES end_user (id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS invite_code_id    BIGINT REFERENCES invite_code (id) ON DELETE SET NULL;
ALTER TABLE end_user DROP CONSTRAINT IF EXISTS ck_end_user_email_lower;
ALTER TABLE end_user ADD CONSTRAINT ck_end_user_email_lower CHECK (email IS NULL OR email = lower(email));
CREATE UNIQUE INDEX IF NOT EXISTS uk_end_user_email ON end_user (email) WHERE email IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_end_user_invited_by ON end_user (invited_by) WHERE invited_by IS NOT NULL;

INSERT INTO permission (code, name) VALUES
    ('setting:read',  '查看系统设置'),
    ('setting:write', '修改系统设置'),
    ('content:read',  '查看公告、教程与共享账号'),
    ('content:write', '管理公告、教程与共享账号'),
    ('ticket:read',   '查看工单'),
    ('ticket:write',  '回复与关闭工单')
ON CONFLICT (code) DO NOTHING;
