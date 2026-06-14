-- 000007_payments_out_trade_no.down.sql
-- 回滚 000007 的所有变更。

DROP INDEX IF EXISTS idx_payments_third_party_tx_id_channel;
CREATE UNIQUE INDEX idx_payments_third_party_tx_id
  ON payments(third_party_tx_id)
  WHERE third_party_tx_id IS NOT NULL;

DROP INDEX IF EXISTS idx_payments_out_trade_no;
DROP INDEX IF EXISTS idx_payments_active_out_trade_no_channel;

ALTER TABLE payments
  DROP COLUMN out_trade_no;
