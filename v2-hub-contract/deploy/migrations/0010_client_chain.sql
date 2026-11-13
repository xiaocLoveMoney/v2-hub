-- 0010：M6 客户端链式——自有落地节点与链式规则（docs/03 §5.18）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。

-- 自有落地节点（docs/03 §5.18，Ruling-37）：用户自己添加、只用作链路落地。链接含凭据，AES-256-GCM 加密，
-- 关联数据 custom_node:<id>:link（先插入空值拿到 id 再回填）；protocol / address / port 是从链接解析出的展示用副本。
CREATE TABLE IF NOT EXISTS custom_node (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT       NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    name       VARCHAR(64)  NOT NULL,
    protocol   VARCHAR(16)  NOT NULL,
    address    VARCHAR(255) NOT NULL,
    port       INTEGER      NOT NULL,
    link_enc   BYTEA        NOT NULL,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_custom_node_user_name UNIQUE (user_id, name),
    CONSTRAINT ck_custom_node_protocol CHECK (protocol IN ('vless', 'vmess', 'trojan', 'shadowsocks', 'socks')),
    CONSTRAINT ck_custom_node_port CHECK (port BETWEEN 1 AND 65535)
);

-- 链式规则（docs/03 §5.18，Ruling-36）：入口节点 → 落地（平台节点或自有落地节点，恰好一个）。
-- kind = client 只在订阅里表达（M6）；server 留给服务端链式（M7）。状态是计算值，不落库。
-- 平台节点或自有落地节点被删除时规则随之删除；每个用户的条数上限由服务端校验。
CREATE TABLE IF NOT EXISTS chain_rule (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id        BIGINT      NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    kind           VARCHAR(16) NOT NULL DEFAULT 'client',
    name           VARCHAR(64) NOT NULL DEFAULT '',          -- 空 = 显示“入口名 → 落地名”
    entry_node_id  BIGINT      NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    exit_node_id   BIGINT      REFERENCES node (id) ON DELETE CASCADE,
    exit_custom_id BIGINT      REFERENCES custom_node (id) ON DELETE CASCADE,
    is_enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_chain_rule_kind CHECK (kind IN ('client', 'server')),
    CONSTRAINT ck_chain_rule_exit CHECK (num_nonnulls(exit_node_id, exit_custom_id) = 1),
    CONSTRAINT ck_chain_rule_distinct CHECK (exit_node_id IS NULL OR exit_node_id <> entry_node_id),
    CONSTRAINT uk_chain_rule_route UNIQUE NULLS NOT DISTINCT (user_id, kind, entry_node_id, exit_node_id, exit_custom_id)
);
CREATE INDEX IF NOT EXISTS idx_chain_rule_entry_node ON chain_rule (entry_node_id);
CREATE INDEX IF NOT EXISTS idx_chain_rule_exit_node ON chain_rule (exit_node_id) WHERE exit_node_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_chain_rule_exit_custom ON chain_rule (exit_custom_id) WHERE exit_custom_id IS NOT NULL;
