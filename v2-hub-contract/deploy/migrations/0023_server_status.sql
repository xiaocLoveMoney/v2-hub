-- 0023：服务器状态卡片（docs/03 §5.1、§5.20，Ruling-83 – Ruling-86）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。
-- 公网 IP、账单与流量额度、网卡计数基线、延迟检测时间
ALTER TABLE server ADD COLUMN IF NOT EXISTS public_ipv4 VARCHAR(45) NOT NULL DEFAULT '';
ALTER TABLE server ADD COLUMN IF NOT EXISTS public_ipv6 VARCHAR(45) NOT NULL DEFAULT '';
ALTER TABLE server ADD COLUMN IF NOT EXISTS price_cents INTEGER;
ALTER TABLE server ADD COLUMN IF NOT EXISTS price_currency VARCHAR(3) NOT NULL DEFAULT 'USD';
ALTER TABLE server ADD COLUMN IF NOT EXISTS billing_cycle VARCHAR(16) NOT NULL DEFAULT 'month';
ALTER TABLE server ADD COLUMN IF NOT EXISTS expires_on DATE;
ALTER TABLE server ADD COLUMN IF NOT EXISTS traffic_quota_bytes BIGINT;
ALTER TABLE server ADD COLUMN IF NOT EXISTS traffic_reset_day SMALLINT NOT NULL DEFAULT 1;
ALTER TABLE server ADD COLUMN IF NOT EXISTS traffic_mode VARCHAR(8) NOT NULL DEFAULT 'both';
ALTER TABLE server ADD COLUMN IF NOT EXISTS traffic_adjust_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE server ADD COLUMN IF NOT EXISTS traffic_adjust_cycle DATE;
ALTER TABLE server ADD COLUMN IF NOT EXISTS nic_last_sent BIGINT;
ALTER TABLE server ADD COLUMN IF NOT EXISTS nic_last_recv BIGINT;
ALTER TABLE server ADD COLUMN IF NOT EXISTS latency_checked_at TIMESTAMPTZ;
ALTER TABLE server DROP CONSTRAINT IF EXISTS ck_server_price_cents;
ALTER TABLE server ADD CONSTRAINT ck_server_price_cents CHECK (price_cents IS NULL OR price_cents BETWEEN 0 AND 99999999);
ALTER TABLE server DROP CONSTRAINT IF EXISTS ck_server_price_currency;
ALTER TABLE server ADD CONSTRAINT ck_server_price_currency CHECK (price_currency ~ '^[A-Z]{3}$');
ALTER TABLE server DROP CONSTRAINT IF EXISTS ck_server_billing_cycle;
ALTER TABLE server ADD CONSTRAINT ck_server_billing_cycle CHECK (billing_cycle IN ('month', 'quarter', 'half_year', 'year', 'once'));
ALTER TABLE server DROP CONSTRAINT IF EXISTS ck_server_traffic_quota;
ALTER TABLE server ADD CONSTRAINT ck_server_traffic_quota CHECK (traffic_quota_bytes IS NULL OR traffic_quota_bytes >= 0);
ALTER TABLE server DROP CONSTRAINT IF EXISTS ck_server_traffic_reset_day;
ALTER TABLE server ADD CONSTRAINT ck_server_traffic_reset_day CHECK (traffic_reset_day BETWEEN 1 AND 31);
ALTER TABLE server DROP CONSTRAINT IF EXISTS ck_server_traffic_mode;
ALTER TABLE server ADD CONSTRAINT ck_server_traffic_mode CHECK (traffic_mode IN ('both', 'out', 'max'));
-- 旧快照是面板原样字段，形状与中立模型不同；清掉，下一次探测成功即写回（至多 30 秒）
UPDATE server SET status_snapshot = NULL WHERE status_snapshot IS NOT NULL AND NOT (status_snapshot ? 'memTotal');

-- 采样加字段（Ruling-85），之前的行为空
ALTER TABLE server_metric ADD COLUMN IF NOT EXISTS mem_used BIGINT;
ALTER TABLE server_metric ADD COLUMN IF NOT EXISTS mem_total BIGINT;
ALTER TABLE server_metric ADD COLUMN IF NOT EXISTS disk_used BIGINT;
ALTER TABLE server_metric ADD COLUMN IF NOT EXISTS disk_total BIGINT;
ALTER TABLE server_metric ADD COLUMN IF NOT EXISTS load1_x100 INTEGER;
ALTER TABLE server_metric ADD COLUMN IF NOT EXISTS net_up_rate BIGINT;
ALTER TABLE server_metric ADD COLUMN IF NOT EXISTS net_down_rate BIGINT;
ALTER TABLE server_metric ADD COLUMN IF NOT EXISTS tcp_count INTEGER;
ALTER TABLE server_metric ADD COLUMN IF NOT EXISTS udp_count INTEGER;

CREATE TABLE IF NOT EXISTS server_nic_traffic_daily (
    server_id BIGINT NOT NULL REFERENCES server (id) ON DELETE CASCADE,
    day       DATE   NOT NULL,
    sent      BIGINT NOT NULL DEFAULT 0,
    recv      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (server_id, day)
);
CREATE INDEX IF NOT EXISTS idx_server_nic_traffic_daily_day ON server_nic_traffic_daily (day);

CREATE TABLE IF NOT EXISTS server_latency (
    server_id    BIGINT      NOT NULL REFERENCES server (id) ON DELETE CASCADE,
    isp          VARCHAR(8)  NOT NULL,
    sampled_at   TIMESTAMPTZ NOT NULL,
    avg_ms       INTEGER,
    loss_percent SMALLINT    NOT NULL DEFAULT 0,
    probe_city   VARCHAR(64) NOT NULL DEFAULT '',
    PRIMARY KEY (server_id, isp, sampled_at),
    CONSTRAINT ck_server_latency_isp CHECK (isp IN ('telecom', 'unicom', 'mobile')),
    CONSTRAINT ck_server_latency_loss CHECK (loss_percent BETWEEN 0 AND 100)
);
CREATE INDEX IF NOT EXISTS idx_server_latency_sampled_at ON server_latency (sampled_at);
