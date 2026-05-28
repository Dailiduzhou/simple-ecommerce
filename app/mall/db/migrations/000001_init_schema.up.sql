CREATE EXTENSION IF NOT EXISTS pg_trgm; -- postgres

CREATE TABLE users (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    nickname VARCHAR(50) NOT NULL DEFAULT '',
    real_name VARCHAR(50) NOT NULL DEFAULT '',

    phone_hash VARCHAR(128) NOT NULL,       -- 用于登录查询的手机号摘要 (HMAC-SHA256)
    phone_encrypt VARCHAR(255) NOT NULL,    -- 用于业务展示的手机号密文 (AES-GCM)

    password_hash VARCHAR(255) NOT NULL,    -- 用户密码的单向哈希 (Bcrypt)
    role VARCHAR(10) NOT NULL DEFAULT 'user', -- enum `user` `merchant` `admin`
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_users_phone_hash ON users(phone_hash);

COMMENT ON TABLE users IS '电商系统用户表';
COMMENT ON COLUMN users.id IS '用户全局唯一ID';
COMMENT ON COLUMN users.phone_hash IS '手机号HMAC摘要，用于等值匹配登录';
COMMENT ON COLUMN users.phone_encrypt IS '手机号AES对称加密密文，用于解密展示';
COMMENT ON COLUMN users.password_hash IS 'Bcrypt加密后的密码';

CREATE TABLE shipping_addresses (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL,
  receiver_name VARCHAR(50) NOT NULL,
  receiver_phone_hash VARCHAR(128) NOT NULL,
  receiver_phone_encrypt VARCHAR(255) NOT NULL,
  province VARCHAR(30) NOT NULL,
  city VARCHAR(30) NOT NULL,
  district VARCHAR(30) NOT NULL,
  detail_address VARCHAR(255) NOT NULL,
  address_tag VARCHAR(20),
  is_default BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT fk_user_addresses
  FOREIGN KEY (user_id) REFERENCES users (id)
  ON DELETE CASCADE
);

CREATE INDEX idx_shipping_addresses_user_id ON shipping_addresses (user_id);
CREATE INDEX idx_shipping_addresses_default ON shipping_addresses (user_id, is_default)
  WHERE is_default = TRUE;

CREATE TABLE categories (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    parent_id BIGINT,                                -- NULL 表示顶级分类
    name VARCHAR(100) NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT fk_category_parent
    FOREIGN KEY (parent_id) REFERENCES categories (id)
    ON DELETE SET NULL
);

CREATE INDEX idx_categories_parent_id ON categories(parent_id);

CREATE TABLE products (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    category_id BIGINT NOT NULL,
    name VARCHAR(255) NOT NULL,

    price NUMERIC(10, 2) NOT NULL DEFAULT 0.00,
    discount NUMERIC(10, 2) NOT NULL DEFAULT 1.00,
    stock INTEGER NOT NULL DEFAULT 0,
    status SMALLINT NOT NULL DEFAULT 0,           -- 0-下架, 1-上架, 2-违规封禁

    cover_image VARCHAR(512) NOT NULL,
    media_assets JSONB NOT NULL DEFAULT '{}',
    description TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMPTZ,

    CONSTRAINT fk_product_category
    FOREIGN KEY (category_id) REFERENCES categories (id)
);

CREATE INDEX idx_products_category_id ON products(category_id);
CREATE INDEX idx_products_status ON products(status) WHERE deleted_at IS NULL;
CREATE INDEX idx_products_media_assets ON products USING GIN (media_assets);

-- 用户有未结束的 payment 或 order 禁止注销
CREATE TABLE orders (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL,
  address_id BIGINT NOT NULL,
  total_amount NUMERIC(10, 2) NOT NULL DEFAULT 0.00,
  status VARCHAR(20) NOT NULL DEFAULT 'creating', -- creating/paid/shipped/completed/cancelled
  is_completed BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT fk_order_user
  FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_order_address
  FOREIGN KEY (address_id) REFERENCES shipping_addresses (id)
);

CREATE INDEX idx_orders_user_id ON orders(user_id);
CREATE INDEX idx_orders_ongoing ON orders(user_id, is_completed)
  WHERE is_completed = FALSE;
CREATE INDEX idx_orders_done ON orders(user_id, is_completed)
  WHERE is_completed = TRUE;

CREATE TABLE order_items (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id BIGINT NOT NULL,
  product_id BIGINT NOT NULL,
  quantity INTEGER NOT NULL DEFAULT 1,
  unit_price NUMERIC(10, 2) NOT NULL,             -- 下单时快照价格
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT fk_order_item_order
  FOREIGN KEY (order_id) REFERENCES orders (id) ON DELETE CASCADE,
  CONSTRAINT fk_order_item_product
  FOREIGN KEY (product_id) REFERENCES products (id)
);

CREATE INDEX idx_order_items_order_id ON order_items(order_id);
CREATE INDEX idx_order_items_product_id ON order_items(product_id);

CREATE TABLE payments (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  merchant_id BIGINT NOT NULL,
  amount NUMERIC(10, 2) NOT NULL,
  status VARCHAR(20) NOT NULL DEFAULT 'pending',  -- pending/success/failed/refunded
  pay_channel VARCHAR(30) NOT NULL DEFAULT '',    -- wechat/alipay/...
  third_party_tx_id VARCHAR(128),                 -- 第三方支付流水号
  paid_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT fk_payment_order
  FOREIGN KEY (order_id) REFERENCES orders (id),
  CONSTRAINT fk_payment_user
  FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_payment_merchant
  FOREIGN KEY (merchant_id) REFERENCES users (id)
);

CREATE INDEX idx_payments_order_id ON payments(order_id);
CREATE INDEX idx_payments_user_id ON payments(user_id);
CREATE UNIQUE INDEX idx_payments_third_party_tx_id ON payments(third_party_tx_id)
  WHERE third_party_tx_id IS NOT NULL;

CREATE TABLE events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name VARCHAR(255) NOT NULL,

    status SMALLINT NOT NULL DEFAULT 0,           -- 0-未开始, 1-进行中, 2-结束

    start_at TIMESTAMPTZ NOT NULL,
    end_at TIMESTAMPTZ NOT NULL,

    cover_image VARCHAR(512) NOT NULL,
    media_assets JSONB NOT NULL DEFAULT '{}',
    description TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMPTZ
);

CREATE INDEX idx_events_status ON events(status) WHERE deleted_at IS NULL;
CREATE INDEX idx_events_time_range ON events(start_at, end_at) WHERE deleted_at IS NULL;
