package data

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Storage is the current adapter, not the domain storage contract. The same
// adapter works with MinIO and private S3-compatible buckets. Bucket creation,
// public access policy and service-account IAM are deployment responsibilities.
type S3Storage struct {
	client   *minio.Client
	signer   *minio.Client
	bucket   string
	disabled bool
}

func NewObjectStorage(c *conf.Storage) (biz.ObjectStorage, error) {
	if c == nil || c.Provider == "" || c.Provider == "disabled" {
		return &S3Storage{disabled: true}, nil
	}
	if c.Provider != "s3" {
		return nil, fmt.Errorf("unsupported storage provider %q", c.Provider)
	}
	access := os.Getenv(c.AccessKeyEnv)
	secret := os.Getenv(c.SecretKeyEnv)
	if c.Endpoint == "" || c.Bucket == "" || access == "" || secret == "" {
		return nil, fmt.Errorf("S3 endpoint, private bucket and credential environment variables are required")
	}
	// Plaintext endpoints leak the static credentials on every request, so they
	// require an explicit local-development opt-out instead of a silent default.
	if !c.GetUseTls() && !c.GetAllowInsecure() {
		return nil, fmt.Errorf("storage.use_tls is disabled: static S3 credentials would travel in cleartext; enable TLS or set storage.allow_insecure=true for local development only")
	}
	if c.PublicEndpoint != "" && !c.GetPublicUseTls() && !c.GetAllowInsecure() {
		return nil, fmt.Errorf("storage.public_use_tls is disabled: presigned read URLs would travel in cleartext; enable TLS or set storage.allow_insecure=true for local development only")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 15 * time.Second
	region := c.Region
	if region == "" {
		region = "us-east-1"
	}
	client, e := minio.New(c.Endpoint, &minio.Options{Creds: credentials.NewStaticV4(access, secret, ""), Secure: c.UseTls, Region: region, Transport: transport, BucketLookup: minio.BucketLookupPath})
	if e != nil {
		return nil, fmt.Errorf("invalid S3 configuration")
	}
	signer := client
	if c.PublicEndpoint != "" {
		signer, e = minio.New(c.PublicEndpoint, &minio.Options{Creds: credentials.NewStaticV4(access, secret, ""), Secure: c.PublicUseTls, Region: region, Transport: transport, BucketLookup: minio.BucketLookupPath})
		if e != nil {
			return nil, fmt.Errorf("invalid public S3 endpoint")
		}
	}
	return &S3Storage{client: client, signer: signer, bucket: c.Bucket}, nil
}

func NewMediaPolicy(c *conf.Storage) (biz.MediaPolicy, error) {
	p := biz.MediaPolicy{UploadTTL: 10 * time.Minute, ReadTTL: 5 * time.Minute, UnusedRetention: 24 * time.Hour}
	if c.GetUploadTtlSeconds() != 0 {
		p.UploadTTL = time.Duration(c.UploadTtlSeconds) * time.Second
	}
	if c.GetReadTtlSeconds() != 0 {
		p.ReadTTL = time.Duration(c.ReadTtlSeconds) * time.Second
	}
	if c.GetUnusedRetentionHours() != 0 {
		p.UnusedRetention = time.Duration(c.UnusedRetentionHours) * time.Hour
	}
	if p.UploadTTL < time.Minute || p.UploadTTL > time.Hour || p.ReadTTL < time.Second || p.ReadTTL > 15*time.Minute || p.UnusedRetention < 2*time.Hour || p.UnusedRetention > 7*24*time.Hour {
		return p, fmt.Errorf("invalid media TTL policy")
	}
	return p, nil
}

func (s *S3Storage) Location() (string, string) {
	if s.disabled {
		return "disabled", ""
	}
	return "s3", s.bucket
}

func (s *S3Storage) check(ref biz.ObjectRef) error {
	if s.disabled {
		return mediav1.ErrorStorageUnavailable("object storage is disabled")
	}
	if ref.Provider != "s3" || ref.Bucket != s.bucket || ref.Key == "" || strings.Contains(ref.Key, "..") || (!strings.HasPrefix(ref.Key, "images/") && !strings.HasPrefix(ref.Key, "uploads/")) {
		return mediav1.ErrorStorageUnavailable("invalid storage locator")
	}
	return nil
}

func storageError(e error) error {
	if e == nil {
		return nil
	}
	code := minio.ToErrorResponse(e).Code
	if code == "NoSuchKey" || code == "NoSuchObject" {
		return mediav1.ErrorMediaNotReady("uploaded object not found")
	}
	return mediav1.ErrorStorageUnavailable("object storage operation failed")
}

func (s *S3Storage) CreateUpload(ctx context.Context, ref biz.ObjectRef, mime string, size int64, expires time.Time) (biz.UploadGrant, error) {
	var g biz.UploadGrant
	if e := s.check(ref); e != nil {
		return g, e
	}
	if !strings.HasPrefix(ref.Key, "uploads/") || size < 1 || size > biz.MaxImageBytes || !expires.After(time.Now()) {
		return g, mediav1.ErrorInvalidImage("invalid upload grant")
	}
	p := minio.NewPostPolicy()
	for _, e := range []error{p.SetBucket(ref.Bucket), p.SetKey(ref.Key), p.SetContentType(mime), p.SetContentLengthRange(size, size), p.SetExpires(expires)} {
		if e != nil {
			return g, mediav1.ErrorInvalidImage("invalid upload policy")
		}
	}
	u, fields, e := s.signer.PresignedPostPolicy(ctx, p)
	if e != nil {
		return g, storageError(e)
	}
	return biz.UploadGrant{URL: u.String(), Fields: fields, ExpiresAt: expires}, nil
}

func (s *S3Storage) ReadObject(ctx context.Context, ref biz.ObjectRef, max int64) ([]byte, error) {
	if e := s.check(ref); e != nil {
		return nil, e
	}
	o, e := s.client.GetObject(ctx, ref.Bucket, ref.Key, minio.GetObjectOptions{})
	if e != nil {
		return nil, storageError(e)
	}
	defer o.Close()
	b, e := io.ReadAll(io.LimitReader(o, max+1))
	if e != nil {
		return nil, storageError(e)
	}
	if int64(len(b)) > max {
		return nil, mediav1.ErrorInvalidImage("image is too large")
	}
	return b, nil
}

func (s *S3Storage) PutImmutable(ctx context.Context, ref biz.ObjectRef, b []byte, mime string) (bool, error) {
	if e := s.check(ref); e != nil {
		return false, e
	}
	if !strings.HasPrefix(ref.Key, "images/") {
		return false, mediav1.ErrorInvalidImage("invalid final object")
	}
	opts := minio.PutObjectOptions{ContentType: mime, DisableMultipart: true}
	opts.SetMatchETagExcept("*")
	_, e := s.client.PutObject(ctx, ref.Bucket, ref.Key, bytes.NewReader(b), int64(len(b)), opts)
	if minio.ToErrorResponse(e).Code == "PreconditionFailed" {
		return false, nil
	}
	return e == nil, storageError(e)
}

func (s *S3Storage) ReadURL(ctx context.Context, ref biz.ObjectRef, ttl time.Duration) (string, error) {
	if e := s.check(ref); e != nil {
		return "", e
	}
	if !strings.HasPrefix(ref.Key, "images/") {
		return "", mediav1.ErrorMediaNotReady("only verified images can be read")
	}
	u, e := s.signer.PresignedGetObject(ctx, ref.Bucket, ref.Key, ttl, url.Values{"response-content-disposition": []string{"inline"}, "response-cache-control": []string{"private, max-age=0"}})
	if e != nil {
		return "", storageError(e)
	}
	return u.String(), nil
}

func (s *S3Storage) DeleteObject(ctx context.Context, ref biz.ObjectRef) error {
	if e := s.check(ref); e != nil {
		return e
	}
	return storageError(s.client.RemoveObject(ctx, ref.Bucket, ref.Key, minio.RemoveObjectOptions{}))
}
