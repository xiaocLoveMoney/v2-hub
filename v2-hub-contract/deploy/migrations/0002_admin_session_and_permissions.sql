-- 0002：管理员会话表与权限码种子（docs/03 §4、§4.2）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行。

CREATE TABLE IF NOT EXISTS admin_session (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash    BYTEA        NOT NULL,
    admin_id      BIGINT       NOT NULL REFERENCES admin (id) ON DELETE CASCADE,
    csrf_token    VARCHAR(64)  NOT NULL,
    client_ip     INET,
    user_agent    VARCHAR(255),
    expires_at    TIMESTAMPTZ  NOT NULL,
    idle_until    TIMESTAMPTZ  NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_admin_session_token_hash UNIQUE (token_hash)
);
CREATE INDEX IF NOT EXISTS idx_admin_session_admin_id ON admin_session (admin_id);
CREATE INDEX IF NOT EXISTS idx_admin_session_expires_at ON admin_session (expires_at);

INSERT INTO permission (code, name) VALUES
    ('admin:read',   '查看管理员与角色'),
    ('admin:write',  '管理管理员与角色'),
    ('audit:read',   '查看审计日志'),
    ('server:read',  '查看服务器'),
    ('server:write', '管理服务器'),
    ('node:read',    '查看节点'),
    ('node:write',   '管理节点')
ON CONFLICT (code) DO NOTHING;
