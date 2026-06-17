-- 000009_payments_active_order_channel.up.sql
-- 让同一订单在同一渠道的 active payment 由数据库保证幂等。

CREATE UNIQUE INDEX idx_payments_active_order_channel
  ON payments(order_id, pay_channel)
  WHERE status IN ('pending','success');
