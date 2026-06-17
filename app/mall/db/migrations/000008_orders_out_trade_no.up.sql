-- 000008_orders_out_trade_no.up.sql
-- 为 orders 表添加 out_trade_no 列(商户订单号),用于统一支付 API
-- 的 order_no -> order 解析。允许为空(历史订单没有商户号),
-- 新建订单时由服务层写入。PostgreSQL 的 unique index 默认允许多个
-- NULL,因此不必先 backfill。

ALTER TABLE orders
  ADD COLUMN out_trade_no VARCHAR(64);

-- 商户订单号唯一约束。一个商户号同一时刻只能对应一个订单。
CREATE UNIQUE INDEX idx_orders_out_trade_no
  ON orders(out_trade_no)
  WHERE out_trade_no IS NOT NULL;
