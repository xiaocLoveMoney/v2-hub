-- 0026：二级代理（docs/03 §5.23，Ruling-101 – Ruling-104）。幂等，可重复执行；单事务，任一步失败整体回滚。
-- 已按 init.sql 全新建库的环境无需执行；本文件的表结构与 init.sql 对应段落保持一致。只有结构变更，不动存量数据。
BEGIN;

-- 账号类型（Ruling-101）：存量管理员全部是员工；代理不能是所有者
ALTER TABLE admin ADD COLUMN IF NOT EXISTS kind VARCHAR(8) NOT NULL DEFAULT 'staff';
ALTER TABLE admin DROP CONSTRAINT IF EXISTS ck_admin_kind;
ALTER TABLE admin ADD CONSTRAINT ck_admin_kind CHECK (kind IN ('staff', 'agent'));
ALTER TABLE admin DROP CONSTRAINT IF EXISTS ck_admin_owner_staff;
ALTER TABLE admin ADD CONSTRAINT ck_admin_owner_staff CHECK (NOT (is_owner AND kind = 'agent'));

-- 名下归属（Ruling-102）：管理员不物理删除，外键不带级联
ALTER TABLE end_user ADD COLUMN IF NOT EXISTS agent_id BIGINT REFERENCES admin (id);
-- 代理侧的列表、计数都带“未删除”并按创建时间倒序翻页，索引照这个形状建
CREATE INDEX IF NOT EXISTS idx_end_user_agent_id ON end_user (agent_id, created_at DESC, id DESC) WHERE agent_id IS NOT NULL AND deleted_at IS NULL;

-- 代理的邀请码（Ruling-102）：与用户个人码互斥
ALTER TABLE invite_code ADD COLUMN IF NOT EXISTS agent_id BIGINT REFERENCES admin (id);
ALTER TABLE invite_code DROP CONSTRAINT IF EXISTS ck_invite_code_single_owner;
ALTER TABLE invite_code ADD CONSTRAINT ck_invite_code_single_owner CHECK (owner_user_id IS NULL OR agent_id IS NULL);
CREATE INDEX IF NOT EXISTS idx_invite_code_agent_id ON invite_code (agent_id) WHERE agent_id IS NOT NULL;

-- 代理配额（Ruling-103）
CREATE TABLE IF NOT EXISTS agent_profile (
    admin_id           BIGINT       PRIMARY KEY REFERENCES admin (id),
    max_users          INTEGER      NOT NULL DEFAULT 0,
    traffic_pool_total BIGINT       NOT NULL DEFAULT 0,
    traffic_pool_used  BIGINT       NOT NULL DEFAULT 0,
    remark             VARCHAR(255) NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT ck_agent_profile_max_users CHECK (max_users >= 0),
    CONSTRAINT ck_agent_profile_pool CHECK (traffic_pool_total >= 0 AND traffic_pool_used >= 0 AND traffic_pool_used <= traffic_pool_total)
);

CREATE TABLE IF NOT EXISTS agent_plan (
    admin_id BIGINT NOT NULL REFERENCES agent_profile (admin_id) ON DELETE CASCADE,
    plan_id  BIGINT NOT NULL REFERENCES plan (id) ON DELETE CASCADE,
    PRIMARY KEY (admin_id, plan_id)
);
CREATE INDEX IF NOT EXISTS idx_agent_plan_plan_id ON agent_plan (plan_id);

-- 开通记录（Ruling-103）：只追加
CREATE TABLE IF NOT EXISTS agent_allocation (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id      BIGINT       NOT NULL REFERENCES admin (id),
    user_id       BIGINT       REFERENCES end_user (id) ON DELETE SET NULL,
    username      VARCHAR(64)  NOT NULL,
    plan_id       BIGINT       REFERENCES plan (id) ON DELETE SET NULL,
    plan_name     VARCHAR(64)  NOT NULL,
    traffic_bytes BIGINT       NOT NULL,
    duration_days INTEGER      NOT NULL,
    kind          VARCHAR(8)   NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT ck_agent_allocation_kind CHECK (kind IN ('create', 'assign')),
    CONSTRAINT ck_agent_allocation_amounts CHECK (traffic_bytes > 0 AND duration_days > 0)
);
CREATE INDEX IF NOT EXISTS idx_agent_allocation_agent ON agent_allocation (agent_id, created_at DESC);
-- 用户 / 套餐被物理删除时要按这两列找引用行置空，没有索引就是整表顺扫
CREATE INDEX IF NOT EXISTS idx_agent_allocation_user ON agent_allocation (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_agent_allocation_plan ON agent_allocation (plan_id) WHERE plan_id IS NOT NULL;

COMMIT;
