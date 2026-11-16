-- 0018：SSH 托管与面板部署（docs/03 §5.22，Ruling-68 – Ruling-71）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。
-- SSH 凭据（docs/03 §5.22，Ruling-68）：一份凭据可被多台主机引用，被引用时不能删。
-- secret_enc 是私钥（OpenSSH / PEM）或密码，passphrase_enc 是私钥口令；AES-256-GCM，
-- 关联数据 ssh_credential:<id>:secret / ssh_credential:<id>:passphrase。key_fingerprint 是公钥指纹，只用于展示。
CREATE TABLE IF NOT EXISTS ssh_credential (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name            VARCHAR(64)  NOT NULL,
    username        VARCHAR(64)  NOT NULL,
    auth_type       VARCHAR(8)   NOT NULL,                 -- key / password
    secret_enc      BYTEA        NOT NULL,
    passphrase_enc  BYTEA,
    key_fingerprint VARCHAR(64)  NOT NULL DEFAULT '',      -- SHA256:<Base64>；密码认证为空串
    remark          VARCHAR(255) NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_ssh_credential_name UNIQUE (name),
    CONSTRAINT ck_ssh_credential_auth CHECK (auth_type IN ('key', 'password'))
);

-- 托管主机（docs/03 §5.22）：一台能 SSH 登录的机器；装好面板并接入后 server_id 指向对应的服务器。
-- host_key_sha256 是首次连接时记录并固定的主机公钥指纹，空串 = 还没记录；os_id、arch 取自最近一次检测。
-- 删服务器时主机保留、解除关联；删主机时任务历史一并删除。
CREATE TABLE IF NOT EXISTS server_host (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name            VARCHAR(64)  NOT NULL,                 -- 接入时用作服务器名
    address         VARCHAR(255) NOT NULL,                 -- IP 或域名
    ssh_port        INTEGER      NOT NULL DEFAULT 22,
    credential_id   BIGINT       NOT NULL REFERENCES ssh_credential (id) ON DELETE RESTRICT,
    provider_type   VARCHAR(16)  NOT NULL,                 -- 面板类型：xui
    region          VARCHAR(2)   NOT NULL DEFAULT '',
    panel_port      INTEGER,                               -- 安装时指定的面板端口；空 = 随机
    host_key_sha256 VARCHAR(64)  NOT NULL DEFAULT '',
    os_id           VARCHAR(32)  NOT NULL DEFAULT '',
    arch            VARCHAR(16)  NOT NULL DEFAULT '',
    server_id       BIGINT       REFERENCES server (id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_server_host_name UNIQUE (name),
    CONSTRAINT uk_server_host_target UNIQUE (address, ssh_port),
    CONSTRAINT uk_server_host_server UNIQUE (server_id),
    CONSTRAINT ck_server_host_region CHECK (region ~ '^([A-Z]{2})?$'),
    CONSTRAINT ck_server_host_ssh_port CHECK (ssh_port BETWEEN 1 AND 65535),
    CONSTRAINT ck_server_host_panel_port CHECK (panel_port IS NULL OR panel_port BETWEEN 1 AND 65535)
);
CREATE INDEX IF NOT EXISTS idx_server_host_credential ON server_host (credential_id);

-- 主机任务（docs/03 §5.22，Ruling-71）：经 SSH 做的每件事一行。一台主机同时只有一条未结束的任务（部分唯一索引）。
-- log 是脱敏后的步骤日志，上限 64 KB；error_code 是失败原因大类，last_error 是脱敏后的摘要。
CREATE TABLE IF NOT EXISTS host_task (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id     BIGINT       NOT NULL REFERENCES server_host (id) ON DELETE CASCADE,
    kind        VARCHAR(16)  NOT NULL,
    status      VARCHAR(16)  NOT NULL DEFAULT 'queued',
    step        VARCHAR(16)  NOT NULL DEFAULT '',
    error_code  VARCHAR(32)  NOT NULL DEFAULT '',
    last_error  VARCHAR(500) NOT NULL DEFAULT '',
    log         TEXT         NOT NULL DEFAULT '',
    created_by  BIGINT       REFERENCES admin (id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    CONSTRAINT ck_host_task_kind CHECK (kind IN ('provision', 'verify', 'restart_panel', 'restart_core', 'upgrade', 'reissue_token', 'enable_tls', 'diagnose')),
    CONSTRAINT ck_host_task_status CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_host_task_active ON host_task (host_id) WHERE status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS idx_host_task_host ON host_task (host_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_host_task_queued ON host_task (id) WHERE status = 'queued';

INSERT INTO permission (code, name) VALUES
    ('host:read',  '查看 SSH 凭据、托管主机与任务'),
    ('host:write', '管理 SSH 凭据与托管主机、发起部署与运维')
ON CONFLICT (code) DO NOTHING;
