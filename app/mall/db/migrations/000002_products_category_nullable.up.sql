-- 软删除的商品会释放分类引用（products.category_id 置空），缓存失效触发器也要
-- 能处理可空的分类键。
--
-- 000001 只能作用于全新的库：就地改写它的定义不会让已经记录为 migrated 的部署
-- 重新执行，所以这些改动必须作为前向迁移补上。
--
-- 每条语句都可以重复执行，手工补已经处于目标状态的库也安全。

ALTER TABLE products ALTER COLUMN category_id DROP NOT NULL;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'products_category_check'
      AND conrelid = 'products'::regclass
  ) THEN
    -- 已删除的商品可以不保留分类，在架商品仍必须绑定分类。
    ALTER TABLE products
      ADD CONSTRAINT products_category_check
      CHECK (deleted_at IS NOT NULL OR category_id IS NOT NULL);
  END IF;
END;
$$;

-- 保持与 000001 中的定义一致：category_id 可能为 NULL，直接拼接会得到 NULL 生成键。
CREATE OR REPLACE FUNCTION enqueue_projection_invalidation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  current_row RECORD;
  targets TEXT[];
BEGIN
  IF TG_OP = 'DELETE' THEN current_row := OLD; ELSE current_row := NEW; END IF;
  CASE TG_TABLE_NAME
    WHEN 'products' THEN
      targets := ARRAY['product:' || current_row.id || ':gen', 'product:list:gen',
                       'product:category:' || COALESCE(current_row.category_id, 0) || ':gen'];
      IF TG_OP = 'UPDATE' AND OLD.category_id IS DISTINCT FROM NEW.category_id THEN
        targets := array_append(targets, 'product:category:' || COALESCE(OLD.category_id, 0) || ':gen');
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
