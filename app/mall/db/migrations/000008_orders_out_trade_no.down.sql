-- 000008_orders_out_trade_no.down.sql
-- 撤销 orders.out_trade_no 列。

DROP INDEX IF EXISTS idx_orders_out_trade_no;
ALTER TABLE orders
  DROP COLUMN IF EXISTS out_trade_no;
