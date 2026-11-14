-- 0015：平台出口可指定用户（docs/03 §5.21，Ruling-64）——chain_exit.assign_mode 与 chain_exit_user。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。已有出口默认按节点组，行为不变。
ALTER TABLE chain_exit ADD COLUMN IF NOT EXISTS assign_mode VARCHAR(8) NOT NULL DEFAULT 'group';
ALTER TABLE chain_exit DROP CONSTRAINT IF EXISTS ck_chain_exit_assign_mode;
ALTER TABLE chain_exit ADD CONSTRAINT ck_chain_exit_assign_mode CHECK (assign_mode IN ('group', 'user'));

CREATE TABLE IF NOT EXISTS chain_exit_user (
    exit_id BIGINT NOT NULL REFERENCES chain_exit (id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    PRIMARY KEY (exit_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_chain_exit_user_user_id ON chain_exit_user (user_id);
