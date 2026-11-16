-- SSH 托管与面板部署（docs/03 §5.22，Ruling-68 – Ruling-71）----------------------

-- name: ListSSHCredentials :many
SELECT c.id, c.name, c.username, c.auth_type, c.key_fingerprint, c.remark, c.created_at, c.updated_at,
       (SELECT COUNT(*) FROM server_host h WHERE h.credential_id = c.id)::int AS host_count
FROM ssh_credential c
ORDER BY c.id;

-- name: GetSSHCredential :one
SELECT c.id, c.name, c.username, c.auth_type, c.key_fingerprint, c.remark, c.created_at, c.updated_at,
       (SELECT COUNT(*) FROM server_host h WHERE h.credential_id = c.id)::int AS host_count
FROM ssh_credential c
WHERE c.id = $1;

-- name: GetSSHCredentialSecret :one
SELECT id, username, auth_type, secret_enc, passphrase_enc
FROM ssh_credential
WHERE id = $1;

-- name: CreateSSHCredential :one
-- 密文依赖 id 作关联数据，先写占位，同一事务内再 SetSSHCredentialSecret
INSERT INTO ssh_credential (name, username, auth_type, secret_enc, remark)
VALUES ($1, $2, $3, '\x00', $4)
RETURNING id;

-- name: SetSSHCredentialSecret :exec
UPDATE ssh_credential
SET secret_enc = $2, passphrase_enc = $3, key_fingerprint = $4, updated_at = now()
WHERE id = $1;

-- name: UpdateSSHCredential :execrows
UPDATE ssh_credential
SET name = $2, username = $3, auth_type = $4, remark = $5, updated_at = now()
WHERE id = $1;

-- name: DeleteSSHCredential :execrows
DELETE FROM ssh_credential WHERE id = $1;

-- name: ListServerHosts :many
SELECT h.id, h.name, h.address, h.ssh_port, h.credential_id, h.provider_type, h.region, h.panel_port,
       h.host_key_sha256, h.os_id, h.arch, h.server_id, h.created_at,
       c.name AS credential_name, coalesce(s.name, '')::varchar AS server_name
FROM server_host h
JOIN ssh_credential c ON c.id = h.credential_id
LEFT JOIN server s ON s.id = h.server_id
ORDER BY h.id;

-- name: GetServerHost :one
SELECT h.id, h.name, h.address, h.ssh_port, h.credential_id, h.provider_type, h.region, h.panel_port,
       h.host_key_sha256, h.os_id, h.arch, h.server_id, h.created_at,
       c.name AS credential_name, coalesce(s.name, '')::varchar AS server_name
FROM server_host h
JOIN ssh_credential c ON c.id = h.credential_id
LEFT JOIN server s ON s.id = h.server_id
WHERE h.id = $1;

-- name: CreateServerHost :one
INSERT INTO server_host (name, address, ssh_port, credential_id, provider_type, region, panel_port, server_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id;

-- name: UpdateServerHost :execrows
-- 改了地址或端口就清空已固定的主机密钥，下次连接重新记录
UPDATE server_host
SET name = sqlc.arg(name), credential_id = sqlc.arg(credential_id), region = sqlc.arg(region), panel_port = sqlc.narg(panel_port),
    host_key_sha256 = CASE WHEN address = sqlc.arg(address) AND ssh_port = sqlc.arg(ssh_port) THEN host_key_sha256 ELSE '' END,
    address = sqlc.arg(address), ssh_port = sqlc.arg(ssh_port), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: DeleteServerHost :execrows
DELETE FROM server_host WHERE id = $1;

-- name: SetServerHostKey :exec
UPDATE server_host SET host_key_sha256 = $2, updated_at = now() WHERE id = $1;

-- name: SetServerHostDetection :exec
UPDATE server_host SET os_id = $2, arch = $3, updated_at = now() WHERE id = $1;

-- name: SetServerHostServer :exec
UPDATE server_host SET server_id = $2, updated_at = now() WHERE id = $1;

-- name: ListServersByPanelGUID :many
-- 面板标识相同的服务器；部署任务再比对地址，同一台才关联而不重复新建
SELECT id, base_url FROM server WHERE panel_guid = $1 ORDER BY id;

-- name: UpdateServerConn :execrows
-- 部署与运维任务改写服务器的接入信息（重签令牌、配置证书、接管已知面板）；令牌另由 SetServerToken 写
UPDATE server
SET base_url = $2, tls_mode = $3, cert_pin_sha256 = $4, updated_at = now()
WHERE id = $1;

-- name: CreateHostTask :one
INSERT INTO host_task (host_id, kind, created_by)
VALUES ($1, $2, $3)
RETURNING id;

-- name: GetHostTask :one
-- 不带日志（上限 64 KB），列与 ListHostTasks 相同；要日志另取 GetHostTaskLog
SELECT t.id, t.host_id, t.kind, t.status, t.step, t.error_code, t.last_error, t.created_by,
       t.created_at, t.started_at, t.finished_at, coalesce(a.username, '')::varchar AS created_by_name
FROM host_task t
LEFT JOIN admin a ON a.id = t.created_by
WHERE t.id = $1;

-- name: GetHostTaskLog :one
SELECT log FROM host_task WHERE id = $1;

-- name: ListHostTasks :many
-- 不带日志；新的在前
SELECT t.id, t.host_id, t.kind, t.status, t.step, t.error_code, t.last_error, t.created_by,
       t.created_at, t.started_at, t.finished_at, coalesce(a.username, '')::varchar AS created_by_name
FROM host_task t
LEFT JOIN admin a ON a.id = t.created_by
WHERE t.host_id = $1
ORDER BY t.id DESC
LIMIT $2;

-- name: ListLatestHostTasks :many
-- 每台主机最近的一条任务，供主机列表显示
SELECT DISTINCT ON (t.host_id) t.id, t.host_id, t.kind, t.status, t.step, t.error_code, t.last_error, t.created_by,
       t.created_at, t.started_at, t.finished_at, coalesce(a.username, '')::varchar AS created_by_name
FROM host_task t
LEFT JOIN admin a ON a.id = t.created_by
ORDER BY t.host_id, t.id DESC;

-- name: CountActiveHostTasks :one
SELECT COUNT(*) FROM host_task WHERE host_id = $1 AND status IN ('queued', 'running');

-- name: ClaimQueuedHostTasks :many
-- 领取排队的任务：按先后取至多 N 条置为进行中；SKIP LOCKED 让并发领取互不等待
UPDATE host_task
SET status = 'running', started_at = now()
WHERE id IN (
    SELECT q.id FROM host_task q
    WHERE q.status = 'queued'
    ORDER BY q.id
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
RETURNING id;

-- name: SetHostTaskStep :exec
UPDATE host_task SET step = $2 WHERE id = $1;

-- name: AppendHostTaskLog :exec
-- 日志上限 64 KB，超出截掉最早的部分
UPDATE host_task SET log = right(log || sqlc.arg(line)::text, 65536) WHERE id = sqlc.arg(id);

-- name: FinishHostTask :execrows
UPDATE host_task
SET status = sqlc.arg(status), error_code = sqlc.arg(error_code), last_error = sqlc.arg(last_error), finished_at = now()
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: CancelHostTask :execrows
UPDATE host_task SET status = 'canceled', finished_at = now() WHERE id = $1 AND status = 'queued';

-- name: FailRunningHostTasks :execrows
-- 进程重启后，上次没跑完的任务一律记为失败（Ruling-71）
UPDATE host_task
SET status = 'failed', error_code = sqlc.arg(error_code), last_error = sqlc.arg(last_error), finished_at = now()
WHERE status = 'running';

-- name: CountServersByName :one
-- 新建主机时名称不能与已有服务器重名（部署成功后主机名就是服务器名）
SELECT COUNT(*) FROM server WHERE name = $1;
