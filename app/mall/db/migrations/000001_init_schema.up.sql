-- Squashed baseline for disposable local databases. Historical migrations and
-- data backfills are intentionally omitted; this file describes the final schema.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE users (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  nickname VARCHAR(50) NOT NULL DEFAULT '',
  real_name VARCHAR(50) NOT NULL DEFAULT '',
  phone_hash VARCHAR(128) NOT NULL,
  phone_encrypt VARCHAR(255) NOT NULL,
  password_hash VARCHAR(255) NOT NULL,
  role VARCHAR(10) NOT NULL DEFAULT 'user',
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
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX idx_shipping_addresses_user_id ON shipping_addresses(user_id);
CREATE UNIQUE INDEX idx_shipping_addresses_one_default
  ON shipping_addresses(user_id)
  WHERE is_default = TRUE;

CREATE TABLE categories (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  parent_id BIGINT,
  name VARCHAR(100) NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT fk_category_parent
    FOREIGN KEY (parent_id) REFERENCES categories(id) ON DELETE SET NULL
);

CREATE INDEX idx_categories_parent_id ON categories(parent_id);

CREATE TABLE products (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  category_id BIGINT NOT NULL,
  name VARCHAR(255) NOT NULL,
  price_minor BIGINT NOT NULL DEFAULT 0,
  discount NUMERIC(10, 2) NOT NULL DEFAULT 1.00,
  stock INTEGER NOT NULL DEFAULT 0,
  status SMALLINT NOT NULL DEFAULT 0,
  cover_image JSONB NOT NULL DEFAULT '{}',
  media_assets JSONB NOT NULL DEFAULT '{}',
  description TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  deleted_at TIMESTAMPTZ,
  CONSTRAINT fk_product_category
    FOREIGN KEY (category_id) REFERENCES categories(id)
);

CREATE INDEX idx_products_category_id ON products(category_id);
CREATE INDEX idx_products_status ON products(status) WHERE deleted_at IS NULL;
CREATE INDEX idx_products_image_main ON products USING GIN (cover_image);
CREATE INDEX idx_products_media_assets ON products USING GIN (media_assets);

COMMENT ON COLUMN products.status IS '商品状态：0=下架，1=上架；当前业务未定义其他状态值';

CREATE TABLE orders (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL,
  address_id BIGINT NOT NULL,
  total_amount_minor BIGINT NOT NULL DEFAULT 0,
  currency VARCHAR(3) NOT NULL DEFAULT 'CNY',
  status VARCHAR(20) NOT NULL DEFAULT 'creating',
  is_completed BOOLEAN NOT NULL DEFAULT FALSE,
  out_trade_no VARCHAR(64),
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT fk_order_user
    FOREIGN KEY (user_id) REFERENCES users(id),
  CONSTRAINT fk_order_address
    FOREIGN KEY (address_id) REFERENCES shipping_addresses(id),
  CONSTRAINT orders_status_check CHECK (
    status IN ('creating', 'pending_payment', 'paid', 'shipped', 'completed', 'cancelling', 'cancelled')
  )
);

CREATE INDEX idx_orders_user_id ON orders(user_id);
CREATE INDEX idx_orders_ongoing ON orders(user_id, is_completed)
  WHERE is_completed = FALSE;
CREATE INDEX idx_orders_done ON orders(user_id, is_completed)
  WHERE is_completed = TRUE;
CREATE UNIQUE INDEX idx_orders_out_trade_no ON orders(out_trade_no)
  WHERE out_trade_no IS NOT NULL;

CREATE TABLE order_items (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id BIGINT NOT NULL,
  product_id BIGINT NOT NULL,
  quantity INTEGER NOT NULL DEFAULT 1,
  unit_price_minor BIGINT NOT NULL,
  product_name_snapshot VARCHAR(255) NOT NULL,
  cover_image_snapshot JSONB NOT NULL DEFAULT '[]',
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT fk_order_item_order
    FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE CASCADE,
  CONSTRAINT fk_order_item_product
    FOREIGN KEY (product_id) REFERENCES products(id)
);

CREATE INDEX idx_order_items_order_id ON order_items(order_id);
CREATE INDEX idx_order_items_product_id ON order_items(product_id);

CREATE TABLE payments (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  merchant_id BIGINT NOT NULL,
  amount_minor BIGINT NOT NULL,
  currency VARCHAR(3) NOT NULL DEFAULT 'CNY',
  status VARCHAR(20) NOT NULL DEFAULT 'pending',
  pay_channel VARCHAR(30) NOT NULL DEFAULT '',
  third_party_tx_id VARCHAR(128),
  out_trade_no VARCHAR(64),
  action_type VARCHAR(20),
  action_payload JSONB,
  paid_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT fk_payment_order
    FOREIGN KEY (order_id) REFERENCES orders(id),
  CONSTRAINT fk_payment_user
    FOREIGN KEY (user_id) REFERENCES users(id),
  CONSTRAINT payments_status_check CHECK (
    status IN ('creating', 'pending', 'success', 'refunded', 'close_pending', 'closed', 'reconcile_required')
  )
);

CREATE INDEX idx_payments_order_id ON payments(order_id);
CREATE INDEX idx_payments_user_id ON payments(user_id);
CREATE UNIQUE INDEX idx_payments_third_party_tx_id_channel
  ON payments(third_party_tx_id, pay_channel)
  WHERE third_party_tx_id IS NOT NULL;
CREATE UNIQUE INDEX idx_payments_out_trade_no_unique
  ON payments(out_trade_no)
  WHERE out_trade_no IS NOT NULL;
CREATE UNIQUE INDEX idx_payments_active_order_method
  ON payments(order_id, pay_channel)
  WHERE status IN ('creating', 'pending', 'success', 'close_pending');

CREATE TABLE order_refunds (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  out_refund_no VARCHAR(64) NOT NULL,
  total_amount_minor BIGINT NOT NULL,
  refund_amount_minor BIGINT NOT NULL,
  currency VARCHAR(3) NOT NULL DEFAULT 'CNY',
  reason TEXT NOT NULL DEFAULT '',
  status VARCHAR(20) NOT NULL DEFAULT 'pending',
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT fk_order_refund_order
    FOREIGN KEY (order_id) REFERENCES orders(id),
  CONSTRAINT fk_order_refund_user
    FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE UNIQUE INDEX idx_order_refunds_out_refund_no ON order_refunds(out_refund_no);
CREATE INDEX idx_order_refunds_order_id ON order_refunds(order_id);
CREATE INDEX idx_order_refunds_user_id ON order_refunds(user_id);
CREATE INDEX idx_order_refunds_status ON order_refunds(status);

CREATE TABLE payment_notifications (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  provider VARCHAR(32) NOT NULL,
  provider_event_id VARCHAR(128),
  out_trade_no VARCHAR(64) NOT NULL,
  payload_hash VARCHAR(64) NOT NULL,
  verified_at TIMESTAMPTZ NOT NULL,
  processed_at TIMESTAMPTZ,
  status VARCHAR(20) NOT NULL DEFAULT 'received',
  last_error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT payment_notifications_status_check CHECK (
    status IN ('received', 'processing', 'processed', 'failed')
  )
);

CREATE UNIQUE INDEX idx_payment_notifications_event
  ON payment_notifications(provider, provider_event_id)
  WHERE provider_event_id IS NOT NULL;
CREATE UNIQUE INDEX idx_payment_notifications_payload
  ON payment_notifications(provider, out_trade_no, payload_hash);

CREATE TABLE payment_reconciliation_failures (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  payment_id BIGINT NOT NULL REFERENCES payments(id),
  provider VARCHAR(32) NOT NULL,
  river_job_id BIGINT,
  attempt INTEGER NOT NULL,
  last_error TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  resolved_at TIMESTAMPTZ,
  CONSTRAINT payment_reconciliation_failures_attempt_check CHECK (attempt > 0)
);

CREATE UNIQUE INDEX idx_payment_reconciliation_failures_job
  ON payment_reconciliation_failures(river_job_id)
  WHERE river_job_id IS NOT NULL;
CREATE INDEX idx_payment_reconciliation_failures_open
  ON payment_reconciliation_failures(payment_id)
  WHERE resolved_at IS NULL;

CREATE TABLE events (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name VARCHAR(255) NOT NULL,
  status SMALLINT NOT NULL DEFAULT 0,
  start_at TIMESTAMPTZ NOT NULL,
  end_at TIMESTAMPTZ NOT NULL,
  cover_image JSONB NOT NULL DEFAULT '[]',
  media_assets JSONB NOT NULL DEFAULT '[]',
  description TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  deleted_at TIMESTAMPTZ
);

CREATE INDEX idx_events_status ON events(status) WHERE deleted_at IS NULL;
CREATE INDEX idx_events_time_range ON events(start_at, end_at) WHERE deleted_at IS NULL;
