-- 0017：总览分析与大陆连通检测（docs/03 §5.20，Ruling-66、Ruling-67）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。
CREATE INDEX IF NOT EXISTS idx_user_traffic_hourly_hour ON user_traffic_hourly (hour);

CREATE TABLE IF NOT EXISTS server_traffic_hourly (
    server_id BIGINT      NOT NULL REFERENCES server (id) ON DELETE CASCADE,
    hour      TIMESTAMPTZ NOT NULL,
    up        BIGINT      NOT NULL DEFAULT 0,
    down      BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (server_id, hour)
);
CREATE INDEX IF NOT EXISTS idx_server_traffic_hourly_hour ON server_traffic_hourly (hour);

CREATE TABLE IF NOT EXISTS node_reach (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    address        VARCHAR(255) NOT NULL,
    port           INTEGER      NOT NULL,
    transport      VARCHAR(4)   NOT NULL,
    verdict        VARCHAR(16)  NOT NULL DEFAULT 'pending',
    cn_ok          INTEGER      NOT NULL DEFAULT 0,
    cn_total       INTEGER      NOT NULL DEFAULT 0,
    control_ok     INTEGER      NOT NULL DEFAULT 0,
    control_total  INTEGER      NOT NULL DEFAULT 0,
    ip_reachable   BOOLEAN,
    probes         JSONB        NOT NULL DEFAULT '[]',
    last_error     VARCHAR(255) NOT NULL DEFAULT '',
    blocked_streak INTEGER      NOT NULL DEFAULT 0,
    blocked_since  TIMESTAMPTZ,
    alert_sent_at  TIMESTAMPTZ,
    checked_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_node_reach_target UNIQUE (address, port, transport),
    CONSTRAINT ck_node_reach_transport CHECK (transport IN ('tcp', 'udp')),
    CONSTRAINT ck_node_reach_verdict CHECK (verdict IN ('pending', 'ok', 'partial', 'blocked', 'port_down', 'unknown')),
    CONSTRAINT ck_node_reach_port CHECK (port BETWEEN 1 AND 65535)
);
