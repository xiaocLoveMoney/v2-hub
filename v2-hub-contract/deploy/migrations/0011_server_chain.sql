-- 0011：M7 服务端链式——套餐开关、服务端规则约束、链路下发记录（docs/03 §5.19、§3 C1–C5）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。

ALTER TABLE plan ADD COLUMN IF NOT EXISTS allow_server_chain BOOLEAN NOT NULL DEFAULT FALSE;

-- 服务端规则的落地只能是平台节点（Ruling-39）；同一用户在同一入口节点上只能有一条服务端规则
ALTER TABLE chain_rule DROP CONSTRAINT IF EXISTS ck_chain_rule_server_exit;
ALTER TABLE chain_rule ADD CONSTRAINT ck_chain_rule_server_exit CHECK (kind = 'client' OR exit_node_id IS NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS uk_chain_rule_server_entry ON chain_rule (user_id, entry_node_id) WHERE kind = 'server';

-- 链路下发记录（docs/03 §5.19，Ruling-40）：每台入口服务器一行，记录面板上平台条目的下发状态。
-- desired_hash 是期望平台条目的 SHA-256；fail_count 只对同一 desired_hash 累计，期望变化即清零；
-- backup_enc 是最近一次写入前的整份 Xray 模板，AES-256-GCM 加密，关联数据 chain_apply:<server_id>:backup。
CREATE TABLE IF NOT EXISTS chain_apply (
    server_id        BIGINT      PRIMARY KEY REFERENCES server (id) ON DELETE CASCADE,
    status           VARCHAR(16) NOT NULL DEFAULT 'pending',
    desired_hash     VARCHAR(64) NOT NULL DEFAULT '',
    applied_rule_ids BIGINT[]    NOT NULL DEFAULT '{}',   -- 当前面板上生效的服务端规则 id
    fail_count       INTEGER     NOT NULL DEFAULT 0,
    last_error       TEXT        NOT NULL DEFAULT '',     -- 脱敏后的最近错误
    last_attempt_at  TIMESTAMPTZ,
    next_attempt_at  TIMESTAMPTZ,                         -- 失败退避到这个时刻；空 = 不限
    applied_at       TIMESTAMPTZ,
    backup_enc       BYTEA,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_chain_apply_status CHECK (status IN ('pending', 'applied', 'failed'))
);
