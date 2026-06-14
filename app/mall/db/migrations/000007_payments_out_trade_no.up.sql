-- 000007_payments_out_trade_no.up.sql
-- 为 payments 表添加 out_trade_no 列，并在非终态上建立唯一索引。
-- 失败/退款的支付可以重新生成 out_trade_no 后重试。

ALTER TABLE payments
  ADD COLUMN out_trade_no VARCHAR(64);

-- 活跃态唯一性: (out_trade_no, pay_channel) 在 pending/success 状态下唯一。
-- 一个商户订单号在同一渠道上被"占用"直到成功或被显式关闭。
CREATE UNIQUE INDEX idx_payments_active_out_trade_no_channel
  ON payments(out_trade_no, pay_channel)
  WHERE status IN ('pending','success') AND out_trade_no IS NOT NULL;

-- 普通索引: 用于按 out_trade_no 查询 (notify / sync 路径)。
CREATE INDEX idx_payments_out_trade_no
  ON payments(out_trade_no)
  WHERE out_trade_no IS NOT NULL;

-- 替换原有的渠道盲唯一索引。Wechat transaction_id 和 Alipay trade_no
-- 共享同一列，必须按 pay_channel 分别约束。
DROP INDEX IF EXISTS idx_payments_third_party_tx_id;
CREATE UNIQUE INDEX idx_payments_third_party_tx_id_channel
  ON payments(third_party_tx_id, pay_channel)
  WHERE third_party_tx_id IS NOT NULL;
