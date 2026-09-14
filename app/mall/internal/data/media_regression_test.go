package data

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestReviewStagingRetryAndGrace(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	ctx := context.Background()
	m := db.MediaAsset{ID: 1, Status: "ready", ObjectKey: "images/retained", StagingKey: "uploads/staging", UploadExpiresAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}
	q.EXPECT().GetMediaAsset(gomock.Any(), int64(1)).DoAndReturn(func(context.Context, int64) (db.MediaAsset, error) { return m, nil }).Times(4)
	calls := 0
	objects := map[string]bool{m.ObjectKey: true, m.StagingKey: true}
	remove := func(_ context.Context, a biz.MediaAsset) error {
		calls++
		if calls == 1 {
			return errors.New("storage unavailable")
		}
		delete(objects, a.StagingKey)
		return nil
	}
	require.ErrorIs(t, removeMediaStaging(ctx, q, 1, remove), biz.ErrMediaCleanupNotDue)
	require.Zero(t, calls)
	m.UploadExpiresAt.Time = time.Now().Add(-2 * time.Minute)
	require.Error(t, removeMediaStaging(ctx, q, 1, remove))
	require.True(t, objects[m.StagingKey])
	q.EXPECT().MarkMediaStagingCleaned(gomock.Any(), int64(1)).DoAndReturn(func(context.Context, int64) error { m.StagingCleaned = true; return nil })
	require.NoError(t, removeMediaStaging(ctx, q, 1, remove))
	require.False(t, objects[m.StagingKey])
	require.True(t, objects[m.ObjectKey])
	require.Equal(t, "ready", m.Status)
	require.NoError(t, removeMediaStaging(ctx, q, 1, remove))
	require.Equal(t, 2, calls)
}
