-- 0009：M5 商店与钱包——时长选项、流量包、优惠码、订单、充值码、充值单、返利、余额流水、代入票据（docs/03 §5.12–§5.17）。幂等，可重复执行。
-- 已按 init.sql 全新建库的环境无需执行；本文件内容与 init.sql 对应段落保持一致。

-- 套餐：免费续期只留在用户上（Ruling-20）；补售卖属性（docs/03 §5.12）。
ALTER TABLE plan
    DROP COLUMN IF EXISTS renew_every_days,
    DROP COLUMN IF EXISTS renew_month_day,
    DROP COLUMN IF EXISTS renew_weekday,
    DROP COLUMN IF EXISTS renew_max,
    ADD COLUMN IF NOT EXISTS is_on_sale  BOOLEAN      NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS is_trial    BOOLEAN      NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS sort_order  INTEGER      NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS description VARCHAR(500) NOT NULL DEFAULT '';

-- 时长选项（docs/03 §5.12，Ruling-29）：套餐的可售规格 = 天数 + 价格（整数分）；同一套餐天数不重复，最多 6 个由服务端校验。
CREATE TABLE IF NOT EXISTS plan_price (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    plan_id       BIGINT      NOT NULL REFERENCES plan (id) ON DELETE CASCADE,
    duration_days INTEGER     NOT NULL,
    price         BIGINT      NOT NULL,                -- 分
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_plan_price_plan_days UNIQUE (plan_id, duration_days),
    CONSTRAINT ck_plan_price_days CHECK (duration_days BETWEEN 1 AND 3650),
    CONSTRAINT ck_plan_price_price CHECK (price BETWEEN 0 AND 99999999)
);

-- 流量包（docs/03 §5.12）：只加额度不改到期。
CREATE TABLE IF NOT EXISTS traffic_pack (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          VARCHAR(64) NOT NULL,
    traffic_bytes BIGINT      NOT NULL,
    price         BIGINT      NOT NULL,                -- 分
    is_on_sale    BOOLEAN     NOT NULL DEFAULT FALSE,
    sort_order    INTEGER     NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_traffic_pack_name UNIQUE (name),
    CONSTRAINT ck_traffic_pack_bytes CHECK (traffic_bytes > 0),
    CONSTRAINT ck_traffic_pack_price CHECK (price BETWEEN 0 AND 99999999)
);

-- 优惠码（docs/03 §5.15）：码存大写；次数判断与占用在下单事务内完成（行加锁）。
CREATE TABLE IF NOT EXISTS coupon (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code              VARCHAR(32)  NOT NULL,
    kind              VARCHAR(8)   NOT NULL,           -- percent / fixed
    value             BIGINT       NOT NULL,           -- percent：1–100；fixed：分
    all_plans         BOOLEAN      NOT NULL DEFAULT TRUE,  -- 假 = 只适用于 coupon_plan 里的套餐
    for_traffic_pack  BOOLEAN      NOT NULL DEFAULT FALSE, -- 是否适用于流量包
    min_amount        BIGINT       NOT NULL DEFAULT 0,     -- 最低消费（分）
    max_uses          INTEGER      NOT NULL DEFAULT 0,     -- 总次数上限，0 不限
    max_uses_per_user INTEGER      NOT NULL DEFAULT 1,     -- 每人次数上限，0 不限
    used_count        INTEGER      NOT NULL DEFAULT 0,
    starts_at         TIMESTAMPTZ,                         -- NULL = 立即生效
    ends_at           TIMESTAMPTZ,                         -- NULL = 不过期
    is_disabled       BOOLEAN      NOT NULL DEFAULT FALSE,
    remark            VARCHAR(255) NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_coupon_code UNIQUE (code),
    CONSTRAINT ck_coupon_kind CHECK (kind IN ('percent', 'fixed')),
    CONSTRAINT ck_coupon_value CHECK (value >= 1 AND (kind <> 'percent' OR value <= 100) AND value <= 99999999)
);

CREATE TABLE IF NOT EXISTS coupon_plan (
    coupon_id BIGINT NOT NULL REFERENCES coupon (id) ON DELETE CASCADE,
    plan_id   BIGINT NOT NULL REFERENCES plan (id) ON DELETE CASCADE,
    PRIMARY KEY (coupon_id, plan_id)
);

-- 订单（docs/03 §5.13，Ruling-28）：只有“已支付”一种状态。用户删除后保留，user_id 置空、username 留快照（Ruling-32）。
CREATE TABLE IF NOT EXISTS shop_order (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_no        VARCHAR(24)  NOT NULL,             -- O + yyyyMMddHHmmss + 6 位随机数字（docs/03 §6）
    user_id         BIGINT       REFERENCES end_user (id) ON DELETE SET NULL,
    username        VARCHAR(64)  NOT NULL,             -- 下单时的用户名
    kind            VARCHAR(16)  NOT NULL,             -- plan / traffic_pack / renew
    plan_id         BIGINT       REFERENCES plan (id) ON DELETE SET NULL,
    traffic_pack_id BIGINT       REFERENCES traffic_pack (id) ON DELETE SET NULL,
    item            JSONB        NOT NULL,             -- 商品快照：名称、天数、流量、单价
    is_trial        BOOLEAN      NOT NULL DEFAULT FALSE,
    amount_original BIGINT       NOT NULL,             -- 原价（分）
    amount_discount BIGINT       NOT NULL DEFAULT 0,   -- 优惠（分）
    amount_paid     BIGINT       NOT NULL,             -- 实付（分）= 原价 − 优惠
    coupon_id       BIGINT       REFERENCES coupon (id) ON DELETE SET NULL,
    coupon_code     VARCHAR(32)  NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_shop_order_no UNIQUE (order_no),
    CONSTRAINT ck_shop_order_kind CHECK (kind IN ('plan', 'traffic_pack', 'renew')),
    CONSTRAINT ck_shop_order_amounts CHECK (amount_original >= 0 AND amount_discount >= 0 AND amount_paid = amount_original - amount_discount AND amount_paid >= 0)
);
CREATE INDEX IF NOT EXISTS idx_shop_order_user ON shop_order (user_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_shop_order_created_at ON shop_order (created_at);
-- 试用套餐每个用户终身只能下单一次（docs/03 §5.12）
CREATE UNIQUE INDEX IF NOT EXISTS uk_shop_order_trial ON shop_order (user_id) WHERE is_trial;

-- 优惠码使用记录：每人次数上限按它统计。
CREATE TABLE IF NOT EXISTS coupon_usage (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    coupon_id  BIGINT      NOT NULL REFERENCES coupon (id) ON DELETE CASCADE,
    user_id    BIGINT      REFERENCES end_user (id) ON DELETE SET NULL,
    order_id   BIGINT      NOT NULL REFERENCES shop_order (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_coupon_usage_order UNIQUE (order_id)
);
CREATE INDEX IF NOT EXISTS idx_coupon_usage_coupon_user ON coupon_usage (coupon_id, user_id);

-- 充值码（docs/03 §5.15，Ruling-31）：只存 HMAC（查找）与 AES-GCM 密文（关联数据 topup_code:<aad_key>），不存明文。
CREATE TABLE IF NOT EXISTS topup_code (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code_hash        BYTEA        NOT NULL,
    code_enc         BYTEA        NOT NULL,
    aad_key          VARCHAR(32)  NOT NULL,            -- 随机值，只作加密关联数据
    code_tail        VARCHAR(4)   NOT NULL,            -- 末 4 位，列表里显示
    amount           BIGINT       NOT NULL,            -- 面值（分）
    batch            VARCHAR(64)  NOT NULL DEFAULT '', -- 批次备注
    expires_at       TIMESTAMPTZ,                      -- NULL = 不过期
    is_disabled      BOOLEAN      NOT NULL DEFAULT FALSE,
    used_by          BIGINT       REFERENCES end_user (id) ON DELETE SET NULL,
    used_by_username VARCHAR(64)  NOT NULL DEFAULT '',
    used_at          TIMESTAMPTZ,                      -- 非空 = 已使用
    created_by       BIGINT       REFERENCES admin (id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_topup_code_hash UNIQUE (code_hash),
    CONSTRAINT ck_topup_code_amount CHECK (amount BETWEEN 1 AND 99999999)
);
CREATE INDEX IF NOT EXISTS idx_topup_code_created_at ON topup_code (created_at DESC);

-- 充值单（docs/03 §5.15）：经支付网关给余额充值；(gateway, gateway_trade_no) 唯一，重复回调只入账一次。
CREATE TABLE IF NOT EXISTS payment (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    trade_no         VARCHAR(24)  NOT NULL,            -- P + yyyyMMddHHmmss + 6 位随机数字
    user_id          BIGINT       REFERENCES end_user (id) ON DELETE SET NULL,
    username         VARCHAR(64)  NOT NULL,
    gateway          VARCHAR(32)  NOT NULL,
    amount           BIGINT       NOT NULL,            -- 分
    status           VARCHAR(16)  NOT NULL DEFAULT 'pending', -- pending / paid / expired
    gateway_trade_no VARCHAR(128),
    expires_at       TIMESTAMPTZ  NOT NULL,            -- 未支付的过期时间
    paid_at          TIMESTAMPTZ,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uk_payment_trade_no UNIQUE (trade_no),
    CONSTRAINT ck_payment_status CHECK (status IN ('pending', 'paid', 'expired')),
    CONSTRAINT ck_payment_amount CHECK (amount BETWEEN 1 AND 99999999)
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_payment_gateway_trade ON payment (gateway, gateway_trade_no) WHERE gateway_trade_no IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_payment_user ON payment (user_id, id DESC);

-- 返利（docs/03 §5.16，Ruling-34）：一笔订单最多一条。
CREATE TABLE IF NOT EXISTS rebate (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    inviter_id       BIGINT      REFERENCES end_user (id) ON DELETE SET NULL,
    inviter_username VARCHAR(64) NOT NULL,
    invitee_id       BIGINT      REFERENCES end_user (id) ON DELETE SET NULL,
    invitee_username VARCHAR(64) NOT NULL,
    order_id         BIGINT      NOT NULL REFERENCES shop_order (id) ON DELETE CASCADE,
    order_amount     BIGINT      NOT NULL,             -- 订单实付（分）
    percent          INTEGER     NOT NULL,             -- 当时的返利比例
    amount           BIGINT      NOT NULL,             -- 返利（分）
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_rebate_order UNIQUE (order_id),
    CONSTRAINT ck_rebate_amount CHECK (amount > 0)
);
CREATE INDEX IF NOT EXISTS idx_rebate_inviter ON rebate (inviter_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_rebate_invitee ON rebate (invitee_id);

-- 余额流水（docs/03 §5.14）：每次余额变动一条，与改余额同一事务。用户删除后保留（Ruling-32）。
CREATE TABLE IF NOT EXISTS wallet_ledger (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       BIGINT       REFERENCES end_user (id) ON DELETE SET NULL,
    username      VARCHAR(64)  NOT NULL,
    kind          VARCHAR(16)  NOT NULL,               -- topup_code / topup_online / purchase / renew / rebate / admin_adjust / forfeit
    amount        BIGINT       NOT NULL,               -- 带符号（分）
    balance_after BIGINT       NOT NULL,
    ref_type      VARCHAR(16)  NOT NULL DEFAULT '',    -- order / topup_code / payment / rebate；没有关联对象为空串
    ref_id        BIGINT,
    remark        VARCHAR(255) NOT NULL DEFAULT '',
    admin_id      BIGINT       REFERENCES admin (id) ON DELETE SET NULL, -- 管理员调整时的操作人
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT ck_wallet_ledger_kind CHECK (kind IN ('topup_code', 'topup_online', 'purchase', 'renew', 'rebate', 'admin_adjust', 'forfeit')),
    CONSTRAINT ck_wallet_ledger_amount CHECK (amount <> 0 AND balance_after >= 0)
);
CREATE INDEX IF NOT EXISTS idx_wallet_ledger_user ON wallet_ledger (user_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_wallet_ledger_created_at ON wallet_ledger (created_at);

-- 切换为用户身份的一次性票据（docs/03 §5.17）：管理端签发，客户门户兑换成代入会话；只存 SHA-256。
CREATE TABLE IF NOT EXISTS impersonate_ticket (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ticket_hash BYTEA       NOT NULL,
    admin_id    BIGINT      NOT NULL REFERENCES admin (id) ON DELETE CASCADE,
    user_id     BIGINT      NOT NULL REFERENCES end_user (id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uk_impersonate_ticket_hash UNIQUE (ticket_hash)
);

-- 终端用户：余额、扣余额自动续费（记上次购买的时长选项）、提醒邮件的去重标记（docs/03 §5.14、§5.16）。
ALTER TABLE end_user
    ADD COLUMN IF NOT EXISTS balance             BIGINT      NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS auto_renew          BOOLEAN     NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS plan_price_id       BIGINT      REFERENCES plan_price (id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS expire_notified_for TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS traffic_notified    BOOLEAN     NOT NULL DEFAULT FALSE;
ALTER TABLE end_user DROP CONSTRAINT IF EXISTS ck_end_user_balance;
ALTER TABLE end_user ADD CONSTRAINT ck_end_user_balance CHECK (balance BETWEEN 0 AND 9999999999);

-- 代入会话（docs/03 §5.17）：非空 = 管理员切换为该用户身份的只读会话。
ALTER TABLE user_session
    ADD COLUMN IF NOT EXISTS impersonator_admin_id BIGINT REFERENCES admin (id) ON DELETE CASCADE;

INSERT INTO permission (code, name) VALUES
    ('shop:read',     '查看商品定价、流量包与优惠码'),
    ('shop:write',    '管理商品定价、流量包与优惠码'),
    ('finance:read',  '查看订单、流水、充值单、返利与收支统计'),
    ('finance:write', '管理充值码、调整用户余额'),
    ('impersonate:write', '切换为用户身份')
ON CONFLICT (code) DO NOTHING;
