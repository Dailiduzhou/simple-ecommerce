//go:build integration

package data

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Pause after the text query has consumed its rows, before enrichment. A real
// concurrent commit must not mix the old version with new/reordered/no images.
type postSnapshotBarrier struct {
	query           string
	reached, resume chan struct{}
	once            sync.Once
}
type postSnapshotQueryKey struct{}

func (b *postSnapshotBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, postSnapshotQueryKey{}, strings.HasPrefix(d.SQL, "-- name: "+b.query+" "))
}

func (b *postSnapshotBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if hit, _ := ctx.Value(postSnapshotQueryKey{}).(bool); hit {
		b.once.Do(func() {
			close(b.reached)
			select {
			case <-b.resume:
			case <-ctx.Done():
			}
		})
	}
}

func TestPostRepoIntegrationReadSnapshotDuringImageReplacement(t *testing.T) {
	for _, method := range []string{"GetVisiblePost", "ListPosts"} {
		for _, mutation := range []string{"replace", "reorder", "remove", "delete"} {
			t.Run(method+"/"+mutation, func(t *testing.T) {
				f := newCommunityFixture(t)
				first, second := f.asset(t, f.actor.ID, "ready"), f.asset(t, f.actor.ID, "ready")
				p := f.post(t, f.actor, first, second)
				replacement := f.asset(t, f.actor.ID, "ready")
				barrier := &postSnapshotBarrier{query: method, reached: make(chan struct{}), resume: make(chan struct{})}
				var release sync.Once
				defer release.Do(func() { close(barrier.resume) })
				cfg, err := pgxpool.ParseConfig(os.Getenv(integrationPostgresEnv))
				require.NoError(t, err)
				cfg.ConnConfig.Tracer = barrier
				pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
				require.NoError(t, err)
				// Release the barrier before closing the pool even if an assertion fails.
				defer func() { release.Do(func() { close(barrier.resume) }); pool.Close() }()
				repo := NewPostRepo(&Data{pool: pool, q: db.New(pool)}, f.tx, f.media)
				ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
				defer cancel()
				type result struct {
					post *biz.Post
					err  error
				}
				done := make(chan result, 1)
				page := pageFor(t, "posts", 20)
				go func() {
					var out *biz.Post
					var err error
					if method == "GetVisiblePost" {
						out, err = repo.Get(ctx, f.other.ID, p.ID)
					} else {
						var posts []biz.Post
						posts, _, err = repo.List(ctx, f.other.ID, f.actor.ID, page)
						if len(posts) > 0 {
							out = &posts[0]
						}
					}
					done <- result{out, err}
				}()
				select {
				case <-barrier.reached:
				case <-ctx.Done():
					t.Fatal("text query did not reach barrier")
				}
				ids := []int64{replacement}
				switch mutation {
				case "reorder":
					ids = []int64{second, first}
				case "remove":
					ids = nil
				}
				if mutation == "delete" {
					require.NoError(t, f.posts.Delete(ctx, f.actor, p.ID))
				} else {
					updated, err := f.posts.Update(ctx, f.actor, p.ID, biz.PostInput{Title: "new title", Content: "new content", ExpectedVersion: p.Version, ImageIDs: ids})
					require.NoError(t, err)
					require.Equal(t, p.Version+1, updated.Version, "Get inside write must reuse the transaction")
					require.Len(t, updated.Images, len(ids))
				}
				release.Do(func() { close(barrier.resume) })
				var got result
				select {
				case got = <-done:
				case <-ctx.Done():
					t.Fatal("snapshot read did not finish")
				}
				require.NoError(t, got.err)
				require.NotNil(t, got.post)
				require.Equal(t, p.Version, got.post.Version)
				require.Equal(t, p.Title, got.post.Title)
				require.Equal(t, p.Content, got.post.Content)
				require.Len(t, got.post.Images, 2)
				require.Equal(t, first, got.post.Images[0].ID)
				require.Equal(t, second, got.post.Images[1].ID)
			})
		}
	}
}

func TestMediaRepoIntegrationFreshExpiryCannotStarveBehindStaleDeletes(t *testing.T) {
	f := newCommunityFixture(t)
	const batch int32 = 2
	f.media.policy.CleanupBatch = batch
	// More than ten batches of failures: a cooldown alone cannot guarantee that
	// newly expired resources receive any slots on the one-minute schedule.
	for i := 0; i < int(batch)*12; i++ {
		id := f.asset(t, f.actor.ID, "deleting")
		_, err := f.pool.Exec(f.ctx, `UPDATE media_assets SET expires_at=clock_timestamp()-interval '2 days',updated_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, id)
		require.NoError(t, err)
	}
	stale := append([]int64{}, f.mediaIDs...)
	for round := 0; round < 12; round++ {
		fresh := []int64{f.asset(t, f.actor.ID, "pending"), f.asset(t, f.actor.ID, "ready")}
		_, err := f.pool.Exec(f.ctx, `UPDATE media_assets SET expires_at=clock_timestamp()-interval '1 hour' WHERE id=ANY($1::bigint[])`, fresh)
		require.NoError(t, err)
		// Simulate repeated failed deletes becoming eligible again, without sleep.
		_, err = f.pool.Exec(f.ctx, `UPDATE media_assets SET updated_at=clock_timestamp()-interval '1 hour' WHERE id=ANY($1::bigint[])`, stale)
		require.NoError(t, err)
		n, err := f.media.Sweep(f.ctx)
		require.NoError(t, err)
		require.EqualValues(t, batch*2, n, "independently bounded fresh and stale batches")
		for _, id := range fresh {
			m, err := f.data.DB(f.ctx).GetMediaAsset(f.ctx, id)
			require.NoError(t, err)
			require.Equal(t, "deleting", m.Status)
			var jobs int
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'media_id')::bigint=$2 AND queue='media'`, biz.MediaDeleteKind, id).Scan(&jobs))
			require.Equal(t, 1, jobs)
		}
	}
}
