//go:build integration

package data

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/require"
)

func integrationS3(t *testing.T) *S3Storage {
	t.Helper()
	endpoint := os.Getenv("ECOMMERCE_INTEGRATION_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set ECOMMERCE_INTEGRATION_S3_ENDPOINT for real MinIO verification")
	}
	bucket := os.Getenv("ECOMMERCE_INTEGRATION_S3_BUCKET")
	require.Contains(t, strings.ToLower(bucket), "integration", "only a dedicated integration bucket is allowed")
	s, e := NewObjectStorage(&conf.Storage{Provider: "s3", Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, AccessKeyEnv: "ECOMMERCE_INTEGRATION_S3_ACCESS_KEY", SecretKeyEnv: "ECOMMERCE_INTEGRATION_S3_SECRET_KEY"})
	require.NoError(t, e)
	storage := s.(*S3Storage)
	ok, e := storage.client.BucketExists(context.Background(), bucket)
	require.NoError(t, e)
	if !ok {
		require.NoError(t, storage.client.MakeBucket(context.Background(), bucket, minio.MakeBucketOptions{Region: "us-east-1"}))
	}
	return storage
}

func postImageForm(t *testing.T, g biz.UploadGrant, b []byte) int {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for k, v := range g.Fields {
		require.NoError(t, form.WriteField(k, v))
	}
	part, e := form.CreateFormFile("file", "image.png")
	require.NoError(t, e)
	_, e = part.Write(b)
	require.NoError(t, e)
	require.NoError(t, form.Close())
	req, e := http.NewRequest(http.MethodPost, g.URL, &body)
	require.NoError(t, e)
	req.Header.Set("Content-Type", form.FormDataContentType())
	client := &http.Client{Timeout: 10 * time.Second}
	resp, e := client.Do(req)
	require.NoError(t, e)
	defer resp.Body.Close()
	_, e = io.Copy(io.Discard, resp.Body)
	require.NoError(t, e)
	return resp.StatusCode
}

func TestCommunityIntegrationRealMinIOUploadPublishCleanup(t *testing.T) {
	storage := integrationS3(t)
	f := newCommunityFixture(t)
	ctx := f.ctx
	policy, e := NewMediaPolicy(nil)
	require.NoError(t, e)
	uc := biz.NewMediaUsecase(f.media, storage, policy)
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 3, 4))))
	b := buf.Bytes()
	m, grant, e := uc.CreateUpload(ctx, f.actor, "image/png", int64(len(b)))
	require.NoError(t, e)
	f.mediaIDs = append(f.mediaIDs, m.ID)
	t.Cleanup(func() {
		for _, key := range []string{m.Object.Key, m.StagingKey} {
			require.NoError(t, storage.client.RemoveObject(ctx, m.Object.Bucket, key, minio.RemoveObjectOptions{}))
		}
	})
	// Complete does not trust declared metadata or another user's ownership.
	_, e = uc.Complete(ctx, f.other, m.ID)
	require.True(t, mediav1.IsMediaForbidden(e))
	require.Less(t, postImageForm(t, grant, bytes.Repeat([]byte("x"), len(b))), 300)
	_, e = uc.Complete(ctx, f.actor, m.ID)
	require.True(t, mediav1.IsInvalidImage(e))
	require.GreaterOrEqual(t, postImageForm(t, grant, append(append([]byte{}, b...), 0)), 400, "signed content-length must be enforced")
	require.Less(t, postImageForm(t, grant, b), 300)
	ready, e := uc.Complete(ctx, f.actor, m.ID)
	require.NoError(t, e)
	require.Equal(t, "ready", ready.Status)
	require.EqualValues(t, 3, ready.Width)
	require.EqualValues(t, 4, ready.Height)
	// Anonymous access to the bucket is denied, while a short-lived signed GET works.
	client := &http.Client{Timeout: 10 * time.Second}
	unsigned := "http://" + os.Getenv("ECOMMERCE_INTEGRATION_S3_ENDPOINT") + "/" + m.Object.Bucket + "/" + m.Object.Key
	resp, e := client.Get(unsigned)
	require.NoError(t, e)
	require.GreaterOrEqual(t, resp.StatusCode, 400)
	resp.Body.Close()
	resp, e = client.Get(ready.URL)
	require.NoError(t, e)
	require.Equal(t, 200, resp.StatusCode)
	read, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, e)
	require.Equal(t, b, read)
	_, e = uc.Get(ctx, f.other, m.ID)
	require.True(t, mediav1.IsMediaNotFound(e))
	post := f.post(t, f.actor, m.ID)
	_, e = uc.Get(ctx, f.other, m.ID)
	require.NoError(t, e)
	// A still-valid credential can overwrite ONLY staging. Repeated completion
	// cannot replace the verified final bytes, nor change their dimensions.
	require.Less(t, postImageForm(t, grant, bytes.Repeat([]byte("x"), len(b))), 300)
	again, e := uc.Complete(ctx, f.actor, m.ID)
	require.NoError(t, e)
	require.Equal(t, ready.Width, again.Width)
	final, e := storage.ReadObject(ctx, m.Object, biz.MaxImageBytes)
	require.NoError(t, e)
	require.Equal(t, b, final)
	created, e := storage.PutImmutable(ctx, m.Object, []byte("not an image"), "image/png")
	require.NoError(t, e)
	require.False(t, created)
	final, e = storage.ReadObject(ctx, m.Object, biz.MaxImageBytes)
	require.NoError(t, e)
	require.Equal(t, b, final)
	// Replace with another genuinely uploaded resource, not a seeded URL. Editing
	// unbinds the old image and schedules its cleanup before the post is deleted.
	replacement, replacementGrant, e := uc.CreateUpload(ctx, f.actor, "image/png", int64(len(b)))
	require.NoError(t, e)
	f.mediaIDs = append(f.mediaIDs, replacement.ID)
	t.Cleanup(func() {
		for _, key := range []string{replacement.Object.Key, replacement.StagingKey} {
			require.NoError(t, storage.client.RemoveObject(ctx, replacement.Object.Bucket, key, minio.RemoveObjectOptions{}))
		}
	})
	require.Less(t, postImageForm(t, replacementGrant, b), 300)
	_, e = uc.Complete(ctx, f.actor, replacement.ID)
	require.NoError(t, e)
	edited, e := biz.NewPostUsecase(f.posts, storage, policy).Update(ctx, f.actor, post.ID, biz.PostInput{Title: "edited", Content: "new image", ImageIDs: []int64{replacement.ID}, ExpectedVersion: post.Version})
	require.NoError(t, e)
	require.EqualValues(t, 2, edited.Version)
	require.NotEmpty(t, edited.Images[0].URL)
	_, e = uc.Get(ctx, f.actor, m.ID)
	require.True(t, mediav1.IsMediaNotFound(e))
	require.NoError(t, f.posts.Delete(ctx, f.actor, post.ID))
	_, e = uc.Get(ctx, f.actor, m.ID)
	require.True(t, mediav1.IsMediaNotFound(e))
	require.ErrorIs(t, uc.Remove(ctx, m.ID), biz.ErrMediaCleanupNotDue)
	// Test time travel of the cleanup grace, without waiting eleven minutes.
	_, e = f.pool.Exec(ctx, `UPDATE media_assets SET upload_expires_at=clock_timestamp()-interval '2 minutes' WHERE id=ANY($1::bigint[])`, []int64{m.ID, replacement.ID})
	require.NoError(t, e)
	require.NoError(t, uc.Remove(ctx, m.ID))
	require.NoError(t, uc.Remove(ctx, replacement.ID))
	require.NoError(t, uc.Remove(ctx, m.ID))
	_, e = storage.client.StatObject(ctx, m.Object.Bucket, m.Object.Key, minio.StatObjectOptions{})
	require.Equal(t, "NoSuchKey", minio.ToErrorResponse(e).Code)
	_, e = storage.client.StatObject(ctx, m.Object.Bucket, m.StagingKey, minio.StatObjectOptions{})
	require.Equal(t, "NoSuchKey", minio.ToErrorResponse(e).Code)
}

func TestCommunityIntegrationMinIOPolicyCannotChangeKeyOrOutliveExpiry(t *testing.T) {
	s := integrationS3(t)
	ctx := context.Background()
	ref := biz.ObjectRef{Provider: "s3", Bucket: s.bucket, Key: "uploads/policy-integration-" + time.Now().Format("150405.000000000")}
	defer s.DeleteObject(ctx, ref)
	g, e := s.CreateUpload(ctx, ref, "image/png", 1, time.Now().Add(2*time.Second))
	require.NoError(t, e)
	original := g.Fields["key"]
	g.Fields["key"] = "images/another-users-final"
	require.GreaterOrEqual(t, postImageForm(t, g, []byte{0}), 400)
	g.Fields["key"] = original
	require.Less(t, postImageForm(t, g, []byte{0}), 300)
	time.Sleep(time.Until(g.ExpiresAt) + 150*time.Millisecond)
	require.GreaterOrEqual(t, postImageForm(t, g, []byte{0}), 400)
}
