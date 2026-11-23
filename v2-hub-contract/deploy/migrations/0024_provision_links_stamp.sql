-- 0024：分享链接按用户记摘要（docs/03 §5.4 节点来源，Ruling-92）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。
-- 取链接时该面板的入站 / 对外地址摘要；与当前摘要不同即重取。旧记录为空串，升级后每个用户重取一次
ALTER TABLE provision ADD COLUMN IF NOT EXISTS links_stamp VARCHAR(32) NOT NULL DEFAULT '';
