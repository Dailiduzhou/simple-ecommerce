package biz

import (
	"context"
	"time"
)

// ObjectRef is a stable private locator, never a client-provided URL or key.
// A different provider implements this contract without importing its SDK in biz.
type (
	ObjectRef   struct{ Provider, Bucket, Key string }
	UploadGrant struct {
		URL       string
		Fields    map[string]string
		ExpiresAt time.Time
	}
)
type ObjectStorage interface {
	Location() (provider, bucket string)
	CreateUpload(context.Context, ObjectRef, string, int64, time.Time) (UploadGrant, error)
	ReadObject(context.Context, ObjectRef, int64) ([]byte, error)
	// PutImmutable atomically creates an object only if absent. False means a
	// previous attempt won; callers validate that stored object instead.
	PutImmutable(context.Context, ObjectRef, []byte, string) (bool, error)
	ReadURL(context.Context, ObjectRef, time.Duration) (string, error)
	DeleteObject(context.Context, ObjectRef) error
}
type MediaPolicy struct{ UploadTTL, ReadTTL, UnusedRetention time.Duration }
