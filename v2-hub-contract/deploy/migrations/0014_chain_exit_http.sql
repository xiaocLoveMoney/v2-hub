-- 0014：M10 出口增加 HTTP / HTTPS 代理（docs/03 §5.21，Ruling-51）——放开 chain_exit.protocol 的取值。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。HTTPS 代理存成 protocol = 'http'，是否带 TLS 在链接里。
ALTER TABLE chain_exit DROP CONSTRAINT IF EXISTS ck_chain_exit_protocol;
ALTER TABLE chain_exit ADD CONSTRAINT ck_chain_exit_protocol CHECK (protocol IN ('vless', 'vmess', 'trojan', 'shadowsocks', 'socks', 'http'));
