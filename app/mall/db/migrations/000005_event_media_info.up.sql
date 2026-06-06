ALTER TABLE events
  ALTER COLUMN cover_image DROP NOT NULL,
  ALTER COLUMN cover_image DROP DEFAULT;

ALTER TABLE events
  ALTER COLUMN cover_image TYPE JSONB
  USING CASE
    WHEN cover_image IS NULL OR cover_image = '' THEN '[]'::jsonb
    ELSE jsonb_build_array(jsonb_build_object('OssURL', cover_image))
  END;

ALTER TABLE events
  ALTER COLUMN cover_image SET DEFAULT '[]'::jsonb,
  ALTER COLUMN cover_image SET NOT NULL,
  ALTER COLUMN media_assets SET DEFAULT '[]'::jsonb;

UPDATE events
SET media_assets = CASE
  WHEN media_assets IS NULL OR media_assets = '{}'::jsonb THEN '[]'::jsonb
  WHEN jsonb_typeof(media_assets) = 'array' THEN media_assets
  WHEN jsonb_typeof(media_assets) = 'object' AND media_assets ? 'OssURL' THEN jsonb_build_array(media_assets)
  WHEN jsonb_typeof(media_assets) = 'object' AND media_assets ? 'oss_url' THEN jsonb_build_array(jsonb_build_object(
    'OssURL', media_assets->>'oss_url',
    'BucketName', media_assets->>'bucket_name',
    'ObjectKey', media_assets->>'object_key',
    'ContentType', media_assets->>'content_type',
    'Size', COALESCE((media_assets->>'size')::bigint, 0)
  ))
  WHEN jsonb_typeof(media_assets) = 'object' THEN (
    SELECT COALESCE(jsonb_agg(jsonb_build_object('OssURL', value)), '[]'::jsonb)
    FROM jsonb_each_text(media_assets)
  )
  ELSE '[]'::jsonb
END
WHERE media_assets IS NULL OR jsonb_typeof(media_assets) <> 'array';
