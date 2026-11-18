-- 0020：面板公开证书改在主机上用 acme.sh 签发（docs/03 §5.22，Ruling-74）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。
-- ACME 账号改由主机上的 acme.sh 注册与保存，平台不再存账号密钥。
DROP TABLE IF EXISTS acme_account;
-- cert_renew_at 是主机上 acme.sh 给出的下次续期时间；空 = 还没记下（含 Ruling-74 之前签的证书），下一轮检查就排“配置证书”。
ALTER TABLE server_host ADD COLUMN IF NOT EXISTS cert_renew_at TIMESTAMPTZ;
