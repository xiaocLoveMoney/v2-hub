-- 0021：一键部署入站与 VLESS 流控（docs/03 §5.1，Ruling-76、Ruling-77）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。
-- client_flow：服务器的 VLESS 流控，空串为不带；打开后平台下发的 client 带这个 flow（3x-ui 只在支持它的 VLESS 入站上生效）
ALTER TABLE server ADD COLUMN IF NOT EXISTS client_flow VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE server DROP CONSTRAINT IF EXISTS ck_server_client_flow;
ALTER TABLE server ADD CONSTRAINT ck_server_client_flow CHECK (client_flow IN ('', 'xtls-rprx-vision'));
-- payload：任务参数（deploy_inbounds 的预设、端口与节点组），其它任务为空对象
ALTER TABLE host_task ADD COLUMN IF NOT EXISTS payload JSONB NOT NULL DEFAULT '{}';
ALTER TABLE host_task DROP CONSTRAINT IF EXISTS ck_host_task_kind;
ALTER TABLE host_task ADD CONSTRAINT ck_host_task_kind CHECK (kind IN ('provision', 'verify', 'restart_panel', 'restart_core', 'upgrade', 'reissue_token', 'enable_tls', 'diagnose', 'deploy_inbounds'));
