-- 0020：面板公开证书改在主机上用 acme.sh 签发（docs/03 §5.22，Ruling-74）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。
-- ACME 账号改由主机上的 acme.sh 注册与保存，平台不再存账号密钥。
DROP TABLE IF EXISTS acme_account;
-- cert_renew_at 是下次续期时间：成功时取主机上 acme.sh 给出的，没成功时记为当时（下一轮重试）；空 = 不自动申请。
ALTER TABLE server_host ADD COLUMN IF NOT EXISTS cert_renew_at TIMESTAMPTZ;
-- Ruling-74 之前由平台签的证书：按到期前 30 天续，到时改由主机上的 acme.sh 签发
UPDATE server_host SET cert_renew_at = cert_not_after - INTERVAL '30 days'
WHERE cert_not_after IS NOT NULL AND cert_renew_at IS NULL;
