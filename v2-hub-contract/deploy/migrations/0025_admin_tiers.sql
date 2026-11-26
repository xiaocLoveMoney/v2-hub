-- 0025：管理员分级加固（docs/03 §4，Ruling-95、Ruling-99、Ruling-100）。幂等，可重复执行；单事务，任一步失败整体回滚。
-- 已按 init.sql 全新建库的环境无需执行；本文件的表结构与种子与 init.sql 对应段落保持一致，数据清理部分只对存量库有意义。
BEGIN;

-- 所有者（Ruling-99）：至多一个，且不能是禁用状态；回填为最早创建的启用超管（已有所有者时不动）
ALTER TABLE admin ADD COLUMN IF NOT EXISTS is_owner BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE admin SET is_owner = TRUE, updated_at = now()
WHERE id = (SELECT a.id FROM admin a
            JOIN admin_role ar ON ar.admin_id = a.id
            JOIN role r ON r.id = ar.role_id AND r.code = 'super_admin'
            WHERE a.is_disabled = FALSE
            ORDER BY a.created_at, a.id LIMIT 1)
  AND NOT EXISTS (SELECT 1 FROM admin WHERE is_owner);
CREATE UNIQUE INDEX IF NOT EXISTS uk_admin_owner ON admin (is_owner) WHERE is_owner;
ALTER TABLE admin DROP CONSTRAINT IF EXISTS ck_admin_owner_enabled;
ALTER TABLE admin ADD CONSTRAINT ck_admin_owner_enabled CHECK (NOT (is_owner AND is_disabled));

-- 二次验证（Ruling-100）：NULL = 需要重新验证；存量会话不踢，首次高危操作时补验
ALTER TABLE admin_session ADD COLUMN IF NOT EXISTS reauth_until TIMESTAMPTZ;

-- 超管专属权限码（Ruling-95）：从其它角色里清掉，清掉的内容记进审计，便于核对与回滚
WITH removed AS (
    DELETE FROM role_permission rp USING role r, permission p
    WHERE rp.role_id = r.id AND rp.permission_id = p.id
      AND r.code <> 'super_admin'
      AND p.code IN ('admin:read', 'admin:write', 'host:read', 'host:write')
    RETURNING r.id AS role_id, r.code AS role_code, p.code AS perm_code
)
INSERT INTO audit_log (actor_type, action, target_type, target_id, detail)
SELECT 'system', 'role.update', 'role', role_id::text,
       jsonb_build_object('source', 'migration_0025', 'code', role_code,
                          'removed', jsonb_agg(perm_code ORDER BY perm_code))
FROM removed GROUP BY role_id, role_code;

-- 预设角色（Ruling-95）：编码被自定义角色占用时先给后者改名。
-- role.code 上限 64、后端校验 ^[a-z][a-z0-9_]{2,63}$：截到 36 位 + '_custom_'（8 位）+ id（bigint 至多 19 位）≤ 63
WITH renamed AS (
    UPDATE role SET code = left(code, 36) || '_custom_' || id, updated_at = now()
    WHERE code IN ('operator', 'finance', 'ops', 'partner') AND is_builtin = FALSE
    RETURNING id, code
)
INSERT INTO audit_log (actor_type, action, target_type, target_id, detail)
SELECT 'system', 'role.update', 'role', id::text,
       jsonb_build_object('source', 'migration_0025', 'renamedTo', code)
FROM renamed;
INSERT INTO role (code, name, is_builtin) VALUES
    ('operator', '客服运营', TRUE),
    ('finance',  '财务',     TRUE),
    ('ops',      '运维',     TRUE),
    ('partner',  '合伙人',   TRUE)
ON CONFLICT (code) DO NOTHING;
-- 内置角色的权限由代码定义，不留行
DELETE FROM role_permission rp USING role r WHERE rp.role_id = r.id AND r.is_builtin;

COMMIT;
