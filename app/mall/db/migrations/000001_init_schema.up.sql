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
  auth_version BIGINT NOT NULL DEFAULT 1 CHECK (auth_version > 0),
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
COMMENT ON COLUMN users.auth_version IS '凭证版本；改密原子递增，令牌必须携带验证密码时读取的同一版本';

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
  discount NUMERIC NOT NULL DEFAULT 1.00,
  stock INTEGER NOT NULL DEFAULT 0,
  status SMALLINT NOT NULL DEFAULT 0,
  cover_image JSONB NOT NULL DEFAULT '{}',
  media_assets JSONB NOT NULL DEFAULT '{}',
  description TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  deleted_at TIMESTAMPTZ,
  CONSTRAINT fk_product_category
    FOREIGN KEY (category_id) REFERENCES categories(id),
  CONSTRAINT products_discount_check CHECK (discount > 0 AND discount <= 1 AND discount = round(discount, 2)),
  -- 库存与价格不允许为负：扣减/回补逻辑一旦出错，这里直接拒绝而不是让脏数据落库。
  CONSTRAINT products_stock_check CHECK (stock >= 0),
  CONSTRAINT products_status_check CHECK (status IN (0, 1)),
  CONSTRAINT products_price_minor_check CHECK (price_minor >= 0)
);

CREATE INDEX idx_products_category_id ON products(category_id);
CREATE INDEX idx_products_status ON products(status) WHERE deleted_at IS NULL;
CREATE INDEX idx_products_image_main ON products USING GIN (cover_image);
CREATE INDEX idx_products_media_assets ON products USING GIN (media_assets);

COMMENT ON COLUMN products.status IS '商品状态：0=下架，1=上架；当前业务未定义其他状态值';

CREATE TABLE stock_adjustments (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  product_id BIGINT NOT NULL REFERENCES products(id),
  actor_id BIGINT NOT NULL REFERENCES users(id),
  delta INTEGER NOT NULL CHECK (delta <> 0),
  reason VARCHAR(255) NOT NULL CHECK (length(reason) > 0),
  idempotency_key VARCHAR(64) NOT NULL,
  resulting_stock INTEGER NOT NULL CHECK (resulting_stock >= 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (product_id, idempotency_key)
);

CREATE TABLE orders (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL,
  address_id BIGINT NOT NULL,
  -- Historical source ID only: address-book deletion must not rewrite orders.
  receiver_name VARCHAR(64) NOT NULL,
  receiver_phone_encrypt TEXT NOT NULL,
  shipping_province VARCHAR(64) NOT NULL,
  shipping_city VARCHAR(64) NOT NULL,
  shipping_district VARCHAR(64) NOT NULL,
  shipping_detail_address TEXT NOT NULL,
  paid_payment_id BIGINT,
  total_amount_minor BIGINT NOT NULL,
  currency VARCHAR(3) NOT NULL DEFAULT 'CNY',
  status VARCHAR(20) NOT NULL DEFAULT 'pending_payment',
  is_completed BOOLEAN NOT NULL DEFAULT FALSE,
  out_trade_no VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(64) NOT NULL,
  request_hash VARCHAR(64) NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT fk_order_user
    FOREIGN KEY (user_id) REFERENCES users(id),
  CONSTRAINT orders_shipping_snapshot_check CHECK (
    address_id > 0 AND receiver_name <> '' AND receiver_phone_encrypt <> ''
    AND shipping_detail_address <> ''
  ),
  CONSTRAINT orders_amount_check CHECK (total_amount_minor > 0),
  CONSTRAINT orders_status_check CHECK (
    status IN ('pending_payment', 'paid', 'shipped', 'completed', 'cancelling', 'cancelled', 'refunded')
  ),
  CONSTRAINT orders_completion_check CHECK (
    is_completed = (status IN ('completed', 'cancelled', 'refunded'))
  )
);

-- Append-only through the application. Preserve actors and transition facts
-- independently of subsequent order state or editable address-book records.
CREATE TABLE order_fulfillment_actions (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id BIGINT NOT NULL REFERENCES orders(id),
  actor_id BIGINT NOT NULL REFERENCES users(id),
  action TEXT NOT NULL CHECK (action IN ('ship', 'complete')),
  from_status TEXT NOT NULL,
  to_status TEXT NOT NULL,
  idempotency_key VARCHAR(64) NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 64),
  reason VARCHAR(255) NOT NULL CHECK (length(btrim(reason)) > 0),
  carrier VARCHAR(64) NOT NULL DEFAULT '',
  tracking_number VARCHAR(128) NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
  UNIQUE (order_id, action),
  UNIQUE (order_id, idempotency_key),
  CHECK ((action='ship' AND from_status='paid' AND to_status='shipped' AND length(btrim(carrier))>0 AND length(btrim(tracking_number))>0)
      OR (action='complete' AND from_status='shipped' AND to_status='completed' AND carrier='' AND tracking_number=''))
);

CREATE FUNCTION protect_order_shipping_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF ROW(NEW.address_id, NEW.receiver_name, NEW.receiver_phone_encrypt,
         NEW.shipping_province, NEW.shipping_city, NEW.shipping_district, NEW.shipping_detail_address)
     IS DISTINCT FROM
     ROW(OLD.address_id, OLD.receiver_name, OLD.receiver_phone_encrypt,
         OLD.shipping_province, OLD.shipping_city, OLD.shipping_district, OLD.shipping_detail_address) THEN
    RAISE EXCEPTION 'order shipping snapshot is immutable' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER orders_shipping_snapshot_immutable BEFORE UPDATE ON orders
  FOR EACH ROW EXECUTE FUNCTION protect_order_shipping_snapshot();

CREATE INDEX idx_orders_user_id ON orders(user_id);
CREATE INDEX idx_orders_ongoing ON orders(user_id, is_completed)
  WHERE is_completed = FALSE;
CREATE INDEX idx_orders_done ON orders(user_id, is_completed)
  WHERE is_completed = TRUE;
CREATE UNIQUE INDEX idx_orders_out_trade_no ON orders(out_trade_no);
CREATE UNIQUE INDEX idx_orders_user_idempotency
  ON orders(user_id, idempotency_key);
CREATE INDEX idx_orders_pending_expiry
  ON orders(expires_at)
  WHERE status = 'pending_payment';

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
    FOREIGN KEY (product_id) REFERENCES products(id),
  CONSTRAINT order_items_quantity_check CHECK (quantity > 0),
  CONSTRAINT order_items_price_check CHECK (unit_price_minor >= 0)
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
  status VARCHAR(20) NOT NULL DEFAULT 'creating',
  pay_channel VARCHAR(30) NOT NULL,
  third_party_tx_id VARCHAR(128),
  out_trade_no VARCHAR(64) NOT NULL,
  action_type VARCHAR(20),
  action_payload JSONB,
  paid_at TIMESTAMPTZ,
  reconciliation_status VARCHAR(20) NOT NULL DEFAULT 'none',
  reconciliation_version BIGINT NOT NULL DEFAULT 0 CHECK (reconciliation_version >= 0),
  reconciliation_reason VARCHAR(64),
  reconciliation_detail TEXT,
  prepay_lease_token VARCHAR(64),
  prepay_lease_until TIMESTAMPTZ,
  prepay_attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT fk_payment_order
    FOREIGN KEY (order_id) REFERENCES orders(id),
  CONSTRAINT fk_payment_user
    FOREIGN KEY (user_id) REFERENCES users(id),
  CONSTRAINT payments_amount_check CHECK (amount_minor > 0),
  CONSTRAINT payments_status_check CHECK (
    status IN ('creating', 'pending', 'failed', 'close_pending', 'closed', 'success', 'refunded')
  ),
  CONSTRAINT payments_reconciliation_status_check CHECK (
    reconciliation_status IN ('none', 'required', 'processing', 'resolved')
  )
);

CREATE INDEX idx_payments_order_id ON payments(order_id);
CREATE INDEX idx_payments_user_id ON payments(user_id);
CREATE UNIQUE INDEX idx_payments_third_party_tx_id_channel
  ON payments(third_party_tx_id, pay_channel)
  WHERE third_party_tx_id IS NOT NULL;
CREATE UNIQUE INDEX idx_payments_out_trade_no
  ON payments(out_trade_no);
CREATE UNIQUE INDEX idx_payments_one_active_per_order
  ON payments(order_id)
  WHERE status IN ('creating', 'pending', 'close_pending');
CREATE INDEX idx_payments_reconciliation
  ON payments(reconciliation_status, updated_at)
  WHERE reconciliation_status <> 'none';

-- Keep the payment which funded fulfilment distinct from excess/late money.
ALTER TABLE orders ADD CONSTRAINT fk_order_paid_payment
  FOREIGN KEY (paid_payment_id) REFERENCES payments(id);

CREATE TABLE payment_reconciliation_actions (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  payment_id BIGINT NOT NULL REFERENCES payments(id),
  actor_id BIGINT NOT NULL REFERENCES users(id),
  action TEXT NOT NULL CHECK (action IN ('retry', 'resolve')),
  from_status TEXT NOT NULL CHECK (from_status IN ('required', 'processing')),
  to_status TEXT NOT NULL,
  from_version BIGINT NOT NULL CHECK (from_version > 0),
  to_version BIGINT NOT NULL CHECK (to_version = from_version + 1),
  idempotency_key VARCHAR(64) NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 64),
  reason VARCHAR(255) NOT NULL CHECK (length(btrim(reason)) > 0),
  evidence TEXT NOT NULL DEFAULT '' CHECK (length(evidence) <= 2000),
  river_job_id BIGINT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
  UNIQUE (payment_id, idempotency_key),
  UNIQUE (payment_id, to_version),
  CHECK ((action='retry' AND from_status='required' AND to_status='processing' AND river_job_id IS NOT NULL)
      OR (action='resolve' AND to_status='resolved' AND length(btrim(evidence))>0 AND river_job_id IS NULL))
);

CREATE TABLE order_refunds (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  payment_id BIGINT,
  out_refund_no VARCHAR(64) NOT NULL,
  total_amount_minor BIGINT NOT NULL,
  refund_amount_minor BIGINT NOT NULL,
  currency VARCHAR(3) NOT NULL DEFAULT 'CNY',
  reason TEXT NOT NULL DEFAULT '',
  purpose VARCHAR(32) NOT NULL DEFAULT 'order_cancel_refund',
  status VARCHAR(20) NOT NULL DEFAULT 'pending',
  last_error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT fk_order_refund_order
    FOREIGN KEY (order_id) REFERENCES orders(id),
  CONSTRAINT fk_order_refund_user
    FOREIGN KEY (user_id) REFERENCES users(id),
  CONSTRAINT fk_order_refund_payment
    FOREIGN KEY (payment_id) REFERENCES payments(id),
  -- 退款金额必须是正数且不超过对应的应付总额，避免超退。
  CONSTRAINT order_refunds_total_amount_check CHECK (total_amount_minor > 0),
  CONSTRAINT order_refunds_amount_check CHECK (refund_amount_minor > 0 AND refund_amount_minor <= total_amount_minor),
  CONSTRAINT order_refunds_purpose_check CHECK (
    purpose IN ('order_cancel_refund', 'duplicate_payment_refund', 'late_payment_refund')
  ),
  CONSTRAINT order_refunds_status_check CHECK (status IN ('pending', 'success', 'failed'))
);

CREATE UNIQUE INDEX idx_order_refunds_out_refund_no ON order_refunds(out_refund_no);
CREATE UNIQUE INDEX idx_order_refunds_payment_id
  ON order_refunds(payment_id)
  WHERE payment_id IS NOT NULL;
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
  river_job_id BIGINT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT payment_notifications_status_check CHECK (
    status IN ('received', 'processing', 'processed', 'failed')
  )
);

CREATE UNIQUE INDEX idx_payment_notifications_event
  ON payment_notifications(provider, provider_event_id)
  WHERE provider_event_id IS NOT NULL;
CREATE UNIQUE INDEX idx_payment_notifications_payload
  ON payment_notifications(provider, out_trade_no, payload_hash);
CREATE INDEX idx_payment_notifications_status_updated
  ON payment_notifications(status, updated_at);

CREATE TABLE payment_reconciliation_failures (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  payment_id BIGINT NOT NULL REFERENCES payments(id),
  provider VARCHAR(32) NOT NULL,
  reason VARCHAR(64) NOT NULL,
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
  deleted_at TIMESTAMPTZ,
  -- 活动结束必须晚于开始，否则窗口判定（DB 时钟）会退化成永久有效/永久过期。
  CONSTRAINT events_window_check CHECK (end_at > start_at)
  , CONSTRAINT events_status_check CHECK (status IN (0, 1, 2))
);

CREATE INDEX idx_events_status ON events(status) WHERE deleted_at IS NULL;
CREATE INDEX idx_events_time_range ON events(start_at, end_at) WHERE deleted_at IS NULL;

CREATE TABLE product_browsing_history (
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  product_id BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  first_viewed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_viewed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (user_id, product_id),
  CHECK (last_viewed_at >= first_viewed_at)
);
CREATE INDEX idx_history_recent ON product_browsing_history(user_id, last_viewed_at DESC, product_id DESC);
CREATE INDEX idx_history_expiry ON product_browsing_history(last_viewed_at, user_id, product_id);
CREATE INDEX idx_history_product ON product_browsing_history(product_id);

CREATE TABLE posts (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  author_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  title TEXT NOT NULL,
  content TEXT NOT NULL,
  version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  deleted_at TIMESTAMPTZ,
  CHECK (deleted_at IS NOT NULL OR (char_length(btrim(title)) BETWEEN 1 AND 100 AND char_length(btrim(content)) BETWEEN 1 AND 5000))
);
CREATE INDEX idx_posts_feed ON posts(created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_posts_author_feed ON posts(author_id, created_at DESC, id DESC) WHERE deleted_at IS NULL;
-- Also covers retained soft-deleted posts during physical user deletion.
CREATE INDEX idx_posts_author ON posts(author_id);

CREATE TABLE media_assets (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  owner_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  provider TEXT NOT NULL,
  bucket_name TEXT NOT NULL,
  object_key TEXT NOT NULL,
  staging_key TEXT NOT NULL,
  staging_cleaned BOOLEAN NOT NULL DEFAULT false,
  staging_cleanup_at TIMESTAMPTZ,
  content_type TEXT NOT NULL,
  size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 1 AND 10485760),
  width INTEGER NOT NULL DEFAULT 0 CHECK (width BETWEEN 0 AND 10000),
  height INTEGER NOT NULL DEFAULT 0 CHECK (height BETWEEN 0 AND 10000),
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'deleting', 'deleted')),
  expires_at TIMESTAMPTZ NOT NULL,
  upload_expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (provider, bucket_name, object_key)
);
CREATE INDEX idx_media_expiry ON media_assets(status, expires_at, id);
CREATE INDEX idx_media_stale_deleting ON media_assets(updated_at, id) WHERE status='deleting';
CREATE INDEX idx_media_staging_cleanup ON media_assets(upload_expires_at, id) WHERE status='ready' AND NOT staging_cleaned AND staging_cleanup_at IS NULL;
CREATE INDEX idx_media_staging_retry ON media_assets(staging_cleanup_at, id) WHERE status='ready' AND NOT staging_cleaned AND staging_cleanup_at IS NOT NULL;
CREATE INDEX idx_media_owner ON media_assets(owner_id);

CREATE TABLE post_images (
  post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  media_id BIGINT NOT NULL REFERENCES media_assets(id),
  sort_order INTEGER NOT NULL CHECK (sort_order BETWEEN 0 AND 8),
  PRIMARY KEY (post_id, media_id),
  UNIQUE (media_id),
  UNIQUE (post_id, sort_order)
);
CREATE TABLE post_likes (
  post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (post_id, user_id)
);
CREATE INDEX idx_post_likes_user ON post_likes(user_id, post_id);

CREATE TABLE post_comments (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  author_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  root_comment_id BIGINT,
  reply_to_comment_id BIGINT,
  content TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  deleted_at TIMESTAMPTZ,
  UNIQUE (post_id, id),
  FOREIGN KEY (post_id, root_comment_id) REFERENCES post_comments(post_id, id),
  FOREIGN KEY (post_id, reply_to_comment_id) REFERENCES post_comments(post_id, id),
  CHECK ((root_comment_id IS NULL) = (reply_to_comment_id IS NULL)),
  CHECK (root_comment_id <> id AND reply_to_comment_id <> id),
  CHECK ((deleted_at IS NULL AND char_length(btrim(content)) BETWEEN 1 AND 1000) OR (deleted_at IS NOT NULL AND content = ''))
);
CREATE INDEX idx_comments_roots ON post_comments(post_id, created_at DESC, id DESC) WHERE root_comment_id IS NULL;
CREATE INDEX idx_comments_replies ON post_comments(post_id, root_comment_id, created_at, id);
CREATE INDEX idx_comments_target ON post_comments(post_id, reply_to_comment_id);
CREATE INDEX idx_comments_visible ON post_comments(post_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_comments_author ON post_comments(author_id);

-- Relationships are immutable, so checking the referenced rows at insertion
-- cannot race with a later reparenting into a third level or another thread.
CREATE FUNCTION check_post_comment_relationship() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND (NEW.post_id, NEW.root_comment_id, NEW.reply_to_comment_id)
      IS DISTINCT FROM (OLD.post_id, OLD.root_comment_id, OLD.reply_to_comment_id) THEN
    RAISE EXCEPTION 'comment relationships are immutable' USING ERRCODE = '23514';
  END IF;
  IF NEW.root_comment_id IS NOT NULL THEN
    IF NOT EXISTS (SELECT 1 FROM post_comments r WHERE r.post_id=NEW.post_id AND r.id=NEW.root_comment_id AND r.root_comment_id IS NULL)
       OR NOT EXISTS (SELECT 1 FROM post_comments t WHERE t.post_id=NEW.post_id AND t.id=NEW.reply_to_comment_id AND (t.id=NEW.root_comment_id OR t.root_comment_id=NEW.root_comment_id)) THEN
      RAISE EXCEPTION 'invalid two-level comment relationship' USING ERRCODE = '23514';
    END IF;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER post_comment_relationship BEFORE INSERT OR UPDATE ON post_comments
  FOR EACH ROW EXECUTE FUNCTION check_post_comment_relationship();


-- The outbox and each source mutation commit together, including writes by
-- expiry/reconciliation error handlers. Append rows instead of locking shared
-- generation keys on writes: checkout and refund may lock products/orders in
-- different phases. Recovery workers deduplicate the selected keys in Redis.
CREATE TABLE cache_invalidations (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  generation_keys TEXT[] NOT NULL CHECK (cardinality(generation_keys) BETWEEN 1 AND 5),
  created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE FUNCTION enqueue_projection_invalidation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  current_row RECORD;
  targets TEXT[];
BEGIN
  IF TG_OP = 'DELETE' THEN current_row := OLD; ELSE current_row := NEW; END IF;
  CASE TG_TABLE_NAME
    WHEN 'products' THEN
      targets := ARRAY['product:' || current_row.id || ':gen', 'product:list:gen',
                       'product:category:' || current_row.category_id || ':gen'];
      IF TG_OP = 'UPDATE' AND OLD.category_id <> NEW.category_id THEN
        targets := array_append(targets, 'product:category:' || OLD.category_id || ':gen');
      END IF;
    WHEN 'orders' THEN
      targets := ARRAY['order:user:' || current_row.user_id || ':gen',
                       'order:user:ongoing:' || current_row.user_id || ':gen'];
    WHEN 'payments' THEN
      targets := ARRAY['payment:' || current_row.id || ':gen',
                       'payment:order:' || current_row.order_id || ':gen',
                       'payment:out_trade_no:' || current_row.out_trade_no || ':gen'];
    ELSE RAISE EXCEPTION 'unsupported projection source: %', TG_TABLE_NAME;
  END CASE;
  INSERT INTO cache_invalidations(generation_keys) VALUES (targets);
  RETURN NULL;
END;
$$;
CREATE TRIGGER products_projection_changed AFTER INSERT OR UPDATE OR DELETE ON products
  FOR EACH ROW EXECUTE FUNCTION enqueue_projection_invalidation();
CREATE TRIGGER orders_projection_changed AFTER INSERT OR UPDATE OR DELETE ON orders
  FOR EACH ROW EXECUTE FUNCTION enqueue_projection_invalidation();
CREATE TRIGGER payments_projection_changed AFTER INSERT OR UPDATE OR DELETE ON payments
  FOR EACH ROW EXECUTE FUNCTION enqueue_projection_invalidation();
