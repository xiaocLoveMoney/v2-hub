-- 0013：M9 链式改造——出口（平台出口 + 用户出口）与有序的链式规则（docs/03 §5.18、§5.19、§5.21）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。
-- 升级提示：旧规则迁移后编号会变，每台有服务端规则的起飞服务器在下一轮下发时会重启一次 Xray。

-- 自有落地节点表改名为链式出口；user_id 为空 = 平台出口（Ruling-47）
DO $$
BEGIN
    IF to_regclass('custom_node') IS NOT NULL AND to_regclass('chain_exit') IS NULL THEN
        ALTER TABLE custom_node RENAME TO chain_exit;
        ALTER TABLE chain_exit RENAME CONSTRAINT uk_custom_node_user_name TO uk_chain_exit_user_name;
        ALTER TABLE chain_exit RENAME CONSTRAINT ck_custom_node_protocol TO ck_chain_exit_protocol;
        ALTER TABLE chain_exit RENAME CONSTRAINT ck_custom_node_port TO ck_chain_exit_port;
        ALTER TABLE chain_exit RENAME CONSTRAINT custom_node_user_id_fkey TO chain_exit_user_id_fkey;
        ALTER INDEX custom_node_pkey RENAME TO chain_exit_pkey;
        ALTER SEQUENCE custom_node_id_seq RENAME TO chain_exit_id_seq;
    END IF;
END $$;

ALTER TABLE chain_exit ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE chain_exit ADD COLUMN IF NOT EXISTS remark VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE chain_exit ADD COLUMN IF NOT EXISTS is_enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE chain_exit ADD COLUMN IF NOT EXISTS allow_server BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE chain_exit ADD COLUMN IF NOT EXISTS allow_client BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE chain_exit ADD COLUMN IF NOT EXISTS resolved_ip INET;
ALTER TABLE chain_exit ADD COLUMN IF NOT EXISTS resolved_at TIMESTAMPTZ;
CREATE UNIQUE INDEX IF NOT EXISTS uk_chain_exit_platform_name ON chain_exit (name) WHERE user_id IS NULL;

-- 平台出口加入节点组，沿用“套餐 → 节点组”授权（Ruling-47）
CREATE TABLE IF NOT EXISTS node_group_exit (
    node_group_id BIGINT NOT NULL REFERENCES node_group (id) ON DELETE CASCADE,
    exit_id       BIGINT NOT NULL REFERENCES chain_exit (id) ON DELETE CASCADE,
    PRIMARY KEY (node_group_id, exit_id)
);
CREATE INDEX IF NOT EXISTS idx_node_group_exit_exit_id ON node_group_exit (exit_id);

-- 链式规则：一组起飞节点 → 一个落地，position 越小越优先（Ruling-46）
CREATE TABLE IF NOT EXISTS chain_route (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         BIGINT      NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    kind            VARCHAR(16) NOT NULL,
    position        INTEGER     NOT NULL,
    name            VARCHAR(64) NOT NULL DEFAULT '',
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
CREATE INDEX IF NOT EXISTS idx_chain_route_exit_node ON chain_route (exit_node_id) WHERE exit_node_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_chain_route_exit ON chain_route (exit_id) WHERE exit_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS chain_route_entry (
    route_id      BIGINT NOT NULL REFERENCES chain_route (id) ON DELETE CASCADE,
    entry_node_id BIGINT NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    PRIMARY KEY (route_id, entry_node_id)
);
CREATE INDEX IF NOT EXISTS idx_chain_route_entry_node ON chain_route_entry (entry_node_id);

-- 起飞节点被删光的规则一并删除（docs/03 §5.18 阻断与删除）；节点的删除入口不止一处，放在库里保证不漏
CREATE OR REPLACE FUNCTION chain_route_drop_empty() RETURNS trigger AS $$
BEGIN
    DELETE FROM chain_route r
    WHERE r.id = OLD.route_id
      AND NOT EXISTS (SELECT 1 FROM chain_route_entry e WHERE e.route_id = r.id);
    RETURN NULL;
END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_chain_route_entry_drop_empty ON chain_route_entry;
CREATE TRIGGER trg_chain_route_entry_drop_empty
    AFTER DELETE ON chain_route_entry
    FOR EACH ROW EXECUTE FUNCTION chain_route_drop_empty();

-- 旧规则迁移：一条旧规则 = 只有一个起飞节点的新规则，按旧 id 排顺序；随后删旧表并让下发记录重新对账
DO $$
BEGIN
    IF to_regclass('chain_rule') IS NOT NULL THEN
        ALTER TABLE chain_route ADD COLUMN legacy_rule_id BIGINT;
        INSERT INTO chain_route (user_id, kind, position, name, exit_node_id, exit_id, is_enabled, created_at, updated_at, legacy_rule_id)
        SELECT user_id, kind, row_number() OVER (PARTITION BY user_id, kind ORDER BY id), name, exit_node_id, exit_custom_id,
               is_enabled, created_at, updated_at, id
        FROM chain_rule
        ORDER BY id;
        INSERT INTO chain_route_entry (route_id, entry_node_id)
        SELECT r.id, c.entry_node_id
        FROM chain_route r
        JOIN chain_rule c ON c.id = r.legacy_rule_id;
        ALTER TABLE chain_route DROP COLUMN legacy_rule_id;
        DROP TABLE chain_rule;
        UPDATE chain_apply
        SET desired_hash = '', applied_rule_ids = '{}', fail_count = 0, next_attempt_at = NULL, updated_at = now();
    END IF;
END $$;
