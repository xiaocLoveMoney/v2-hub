-- 0019：DNS 账号与面板的公开证书（docs/03 §5.22，Ruling-72、Ruling-73）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。
-- DNS 账号（docs/03 §5.22，Ruling-72）：DNS 服务商的 API 令牌，一份对应一个根域名；给主机建解析与做证书的 DNS 验证。
-- token_enc 用 AES-256-GCM 加密，关联数据 dns_account:<id>:token；只在 hub-server 上使用，不下发到主机。被主机引用时不能删。
CREATE TABLE IF NOT EXISTS dns_account (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          VARCHAR(64)  NOT NULL,
    provider_type VARCHAR(16)  NOT NULL,                   -- 服务商：cloudflare
    zone          VARCHAR(255) NOT NULL,                   -- 根域名，小写，如 example.com
    token_enc     BYTEA        NOT NULL,
    remark        VARCHAR(255) NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_dns_account_name UNIQUE (name)
);

-- ACME 账号（docs/03 §5.22，Ruling-73）：每个签发方目录地址一把账号密钥，首次申请证书时生成。
-- key_enc 是 PKCS#8 的 ECDSA P-256 私钥，AES-256-GCM 加密，关联数据 acme_account:<directory_url>:key。
CREATE TABLE IF NOT EXISTS acme_account (
    directory_url VARCHAR(255) PRIMARY KEY,
    key_enc       BYTEA        NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);

ALTER TABLE server_host ADD COLUMN IF NOT EXISTS dns_account_id BIGINT REFERENCES dns_account (id) ON DELETE RESTRICT;
ALTER TABLE server_host ADD COLUMN IF NOT EXISTS domain VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE server_host ADD COLUMN IF NOT EXISTS cert_not_after TIMESTAMPTZ;
ALTER TABLE server_host DROP CONSTRAINT IF EXISTS ck_server_host_domain;
ALTER TABLE server_host ADD CONSTRAINT ck_server_host_domain CHECK ((dns_account_id IS NULL) = (domain = ''));
CREATE INDEX IF NOT EXISTS idx_server_host_dns_account ON server_host (dns_account_id);
CREATE UNIQUE INDEX IF NOT EXISTS uk_server_host_domain ON server_host (domain) WHERE domain <> '';
