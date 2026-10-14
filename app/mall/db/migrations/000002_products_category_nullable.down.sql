-- 回退到 000002 之前（旧 000001）的形态：分类列不可空、无分类检查约束、触发器
-- 假定分类键一定存在。
--
-- 旧 schema 无法表示"已软删除且不保留分类"的商品行，遇到这类数据时明确失败，
-- 而不是静默删除业务数据；失败时整个迁移在同一事务中回滚。

DO $$
DECLARE
  orphans BIGINT;
BEGIN
  SELECT count(*) INTO orphans FROM products WHERE category_id IS NULL;
  IF orphans > 0 THEN
    RAISE EXCEPTION '% product row(s) released their category; re-attach or remove them before reverting', orphans;
  END IF;
END;
$$;

CREATE OR REPLACE FUNCTION enqueue_projection_invalidation() RETURNS trigger LANGUAGE plpgsql AS $$
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

ALTER TABLE products DROP CONSTRAINT IF EXISTS products_category_check;
ALTER TABLE products ALTER COLUMN category_id SET NOT NULL;
