-- 0003：管理员两步验证（docs/03 §4.3）。幂等，可重复执行。已按 init.sql 全新建库的环境无需执行。
ALTER TABLE admin ADD COLUMN IF NOT EXISTS totp_pending_enc BYTEA;
ALTER TABLE admin ADD COLUMN IF NOT EXISTS totp_last_step BIGINT NOT NULL DEFAULT 0;
