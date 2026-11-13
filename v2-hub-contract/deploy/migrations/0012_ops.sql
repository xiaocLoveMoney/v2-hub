-- 0012：M8 运营——掉线告警标记、Telegram 发件箱、后台任务状态（docs/03 §5.20）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。

-- 掉线告警（Ruling-43）：离开 online 的时刻与这次掉线是否已告警
ALTER TABLE server ADD COLUMN IF NOT EXISTS offline_since TIMESTAMPTZ;
ALTER TABLE server ADD COLUMN IF NOT EXISTS alert_sent_at TIMESTAMPTZ;

-- Telegram 发件箱（Ruling-42）：纯文本，发出或放弃后清空正文；记录保留 30 天
CREATE TABLE IF NOT EXISTS telegram_outbox (
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
CREATE INDEX IF NOT EXISTS idx_telegram_outbox_queue ON telegram_outbox (next_attempt_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_telegram_outbox_created_at ON telegram_outbox (created_at);

-- 后台任务的少量持久状态（如日报已发到哪一天），一行一个键
CREATE TABLE IF NOT EXISTS job_state (
    key        VARCHAR(64)  PRIMARY KEY,
    value      VARCHAR(255) NOT NULL,
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
