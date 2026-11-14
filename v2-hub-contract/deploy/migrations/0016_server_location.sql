-- 0016：服务器位置改为国家码、节点地区跟随服务器、服务器负载采样（docs/03 §5.1，Ruling-65）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。
-- 老的服务器地区只保留合法的两位字母（转大写），其余清空，由 hub-server 启动时的位置扫描补上。
ALTER TABLE server DROP CONSTRAINT IF EXISTS ck_server_region;
UPDATE server SET region = upper(btrim(region)) WHERE btrim(region) ~ '^[A-Za-z]{2}$' AND region <> upper(btrim(region));
UPDATE server SET region = '' WHERE region !~ '^([A-Z]{2})?$';
ALTER TABLE server ALTER COLUMN region TYPE VARCHAR(2);
ALTER TABLE server ADD CONSTRAINT ck_server_region CHECK (region ~ '^([A-Z]{2})?$');

ALTER TABLE node DROP COLUMN IF EXISTS region;

CREATE TABLE IF NOT EXISTS server_metric (
    server_id      BIGINT           NOT NULL REFERENCES server (id) ON DELETE CASCADE,
    bucket_at      TIMESTAMPTZ      NOT NULL,
    cpu            DOUBLE PRECISION,
    online_clients INTEGER          NOT NULL DEFAULT 0,
    PRIMARY KEY (server_id, bucket_at)
);
CREATE INDEX IF NOT EXISTS idx_server_metric_bucket_at ON server_metric (bucket_at);
