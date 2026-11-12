-- 0005：面板会复用已删入站的标识（docs/03 §5.1 面板标识复用、缺失入站）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；执行后与 init.sql 对应段落一致（0004 的唯一约束由本文件改为部分唯一索引）。

ALTER TABLE inbound DROP CONSTRAINT IF EXISTS uk_inbound_server_remote;
CREATE UNIQUE INDEX IF NOT EXISTS uk_inbound_server_remote ON inbound (server_id, remote_ref) WHERE NOT is_missing;
