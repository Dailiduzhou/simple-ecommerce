package data

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/stretchr/testify/require"
)

func TestStorageFactoryAndSeparateSigningEndpoint(t *testing.T) {
	t.Setenv("TEST_S3_ACCESS", "test-access")
	t.Setenv("TEST_S3_SECRET", "test-secret")
	s, e := NewObjectStorage(nil)
	require.NoError(t, e)
	provider, _ := s.Location()
	require.Equal(t, "disabled", provider)
	_, e = s.ReadURL(context.Background(), biz.ObjectRef{}, time.Minute)
	require.Error(t, e)
	_, e = NewObjectStorage(&conf.Storage{Provider: "unsupported"})
	require.Error(t, e)
	_, e = NewObjectStorage(&conf.Storage{Provider: "s3", Endpoint: "localhost:9000", Bucket: "private"})
	require.Error(t, e)
	// Signing never contacts the public endpoint (nor fetches a client URL).
	_, e = NewObjectStorage(&conf.Storage{Provider: "s3", Endpoint: "minio.internal:9000", UseTls: false, Bucket: "private", AccessKeyEnv: "TEST_S3_ACCESS", SecretKeyEnv: "TEST_S3_SECRET"})
	require.ErrorContains(t, e, "allow_insecure", "plaintext credentials need an explicit opt-in")
	_, e = NewObjectStorage(&conf.Storage{Provider: "s3", Endpoint: "minio.internal:9000", UseTls: true, PublicEndpoint: "images.example.test", PublicUseTls: false, Bucket: "private", AccessKeyEnv: "TEST_S3_ACCESS", SecretKeyEnv: "TEST_S3_SECRET"})
	require.ErrorContains(t, e, "public_use_tls", "plaintext presigned URLs need an explicit opt-in")
	s, e = NewObjectStorage(&conf.Storage{Provider: "s3", Endpoint: "minio.internal:9000", UseTls: true, PublicEndpoint: "images.example.test", PublicUseTls: true, Bucket: "private", AccessKeyEnv: "TEST_S3_ACCESS", SecretKeyEnv: "TEST_S3_SECRET"})
	require.NoError(t, e)
	// The local-development opt-out is honoured for both endpoints.
	_, e = NewObjectStorage(&conf.Storage{Provider: "s3", Endpoint: "localhost:9000", PublicEndpoint: "localhost:9002", AllowInsecure: true, Bucket: "private", AccessKeyEnv: "TEST_S3_ACCESS", SecretKeyEnv: "TEST_S3_SECRET"})
	require.NoError(t, e)
	ref := biz.ObjectRef{Provider: "s3", Bucket: "private", Key: "images/unique"}
	signed, e := s.ReadURL(context.Background(), ref, time.Minute)
	require.NoError(t, e)
	u, e := url.Parse(signed)
	require.NoError(t, e)
	require.Equal(t, "https", u.Scheme)
	require.Equal(t, "images.example.test", u.Host)
	ref.Key = "uploads/unique"
	g, e := s.CreateUpload(context.Background(), ref, "image/png", 100, time.Now().Add(time.Minute))
	require.NoError(t, e)
	require.Contains(t, g.URL, "https://images.example.test")
	ref.Bucket = "foreign"
	_, e = s.ReadURL(context.Background(), ref, time.Minute)
	require.Error(t, e)
}

func TestCommunityAndMediaPoliciesRejectDangerousConfiguration(t *testing.T) {
	for _, c := range []*conf.Community{{HistoryRetentionDays: 2147483647}, {CleanupBatchSize: 1001}, {MediaWorkers: 33}, {UploadsPerMinute: -1}} {
		_, e := NewCommunityPolicy(c)
		require.Error(t, e)
		_, e = NewWriteLimiter(nil, c, nil)
		require.Error(t, e)
	}
	p, e := NewCommunityPolicy(nil)
	require.NoError(t, e)
	require.Equal(t, 90*24*time.Hour, p.HistoryRetention)
	for _, c := range []*conf.Storage{{ReadTtlSeconds: 901}, {UploadTtlSeconds: 3601}, {UnusedRetentionHours: -1}} {
		_, e := NewMediaPolicy(c)
		require.Error(t, e)
	}
}
