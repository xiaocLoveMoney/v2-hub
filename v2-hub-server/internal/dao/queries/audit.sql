-- name: InsertAuditLog :exec
INSERT INTO audit_log (actor_type, actor_id, action, target_type, target_id, detail, client_ip)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListAuditLogs :many
SELECT id, actor_type, actor_id, action, target_type, target_id, detail, client_ip, created_at
FROM audit_log
ORDER BY id DESC
LIMIT $1 OFFSET $2;

-- name: CountAuditLogs :one
SELECT COUNT(*) FROM audit_log;
