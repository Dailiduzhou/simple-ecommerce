CREATE TABLE order_refunds (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  out_refund_no VARCHAR(64) NOT NULL,
  total_amount INTEGER NOT NULL,
  refund_amount INTEGER NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  status VARCHAR(20) NOT NULL DEFAULT 'pending',
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT fk_order_refund_order
  FOREIGN KEY (order_id) REFERENCES orders (id),
  CONSTRAINT fk_order_refund_user
  FOREIGN KEY (user_id) REFERENCES users (id)
);

CREATE UNIQUE INDEX idx_order_refunds_out_refund_no ON order_refunds(out_refund_no);
CREATE INDEX idx_order_refunds_order_id ON order_refunds(order_id);
CREATE INDEX idx_order_refunds_user_id ON order_refunds(user_id);
CREATE INDEX idx_order_refunds_status ON order_refunds(status);
