//go:build integration

package data

import (
	"context"
	"errors"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/stretchr/testify/require"
)

func TestReviewIntegrationBoundStagingCleanup(t *testing.T) {
	f := newCommunityFixture(t)
	id := f.asset(t, f.actor.ID, "ready")
	f.post(t, f.actor, id)
	m, e := f.media.Read(f.ctx, f.actor.ID, id)
	require.NoError(t, e)
	objects := map[string]bool{m.Object.Key: true, m.StagingKey: true}
	attempts := 0
	remove := func(_ context.Context, m biz.MediaAsset) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary storage failure")
		}
		delete(objects, m.StagingKey)
		return nil
	}
	_, e = f.pool.Exec(f.ctx, `UPDATE media_assets SET upload_expires_at=clock_timestamp()+interval '1 minute' WHERE id=$1`, id)
	require.NoError(t, e)
	require.ErrorIs(t, f.media.RemoveStaging(f.ctx, id, remove), biz.ErrMediaCleanupNotDue)
	require.Zero(t, attempts)
	_, e = f.pool.Exec(f.ctx, `UPDATE media_assets SET upload_expires_at=clock_timestamp()-interval '2 minutes' WHERE id=$1`, id)
	require.NoError(t, e)
	_, e = f.media.Sweep(f.ctx)
	require.NoError(t, e)
	var jobs int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'media_id')::bigint=$2 AND (args->>'staging_only')::boolean`, biz.MediaDeleteKind, id).Scan(&jobs))
	require.Equal(t, 1, jobs)
	require.Error(t, f.media.RemoveStaging(f.ctx, id, remove))
	require.True(t, objects[m.StagingKey])
	require.NoError(t, f.media.RemoveStaging(f.ctx, id, remove))
	require.False(t, objects[m.StagingKey])
	require.True(t, objects[m.Object.Key])
	require.NoError(t, f.media.RemoveStaging(f.ctx, id, remove))
	require.Equal(t, 2, attempts)
	retained, e := f.media.Read(f.ctx, f.actor.ID, id)
	require.NoError(t, e)
	require.Equal(t, "ready", retained.Status)
}

func TestStagingCleanupSchedulesBeyondFailedBatch(t *testing.T) {
	f := newCommunityFixture(t)
	f.media.policy.CleanupBatch = 1
	first := f.asset(t, f.actor.ID, "ready")
	f.post(t, f.actor, first)
	second := f.asset(t, f.actor.ID, "ready")
	f.post(t, f.actor, second)
	// First job stays queued (or failing): it must not monopolize every sweep.
	_, e := f.media.Sweep(f.ctx)
	require.NoError(t, e)
	_, e = f.media.Sweep(f.ctx)
	require.NoError(t, e)
	for _, id := range []int64{first, second} {
		var n int
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'media_id')::bigint=$2 AND (args->>'staging_only')::boolean`, biz.MediaDeleteKind, id).Scan(&n))
		require.Equal(t, 1, n)
	}
	// An exhausted old job can be rescheduled, independently of fresh work.
	_, e = f.pool.Exec(f.ctx, `UPDATE river_job SET state='discarded',finalized_at=clock_timestamp() WHERE kind=$1 AND (args->>'media_id')::bigint=$2`, biz.MediaDeleteKind, first)
	require.NoError(t, e)
	_, e = f.pool.Exec(f.ctx, `UPDATE media_assets SET staging_cleanup_at=clock_timestamp()-interval '11 minutes' WHERE id=$1`, first)
	require.NoError(t, e)
	third := f.asset(t, f.actor.ID, "ready")
	f.post(t, f.actor, third)
	_, e = f.media.Sweep(f.ctx)
	require.NoError(t, e)
	for _, id := range []int64{first, third} {
		var n int
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'media_id')::bigint=$2 AND state='available'`, biz.MediaDeleteKind, id).Scan(&n))
		require.Equal(t, 1, n)
	}
}
