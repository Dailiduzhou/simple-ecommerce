//go:build integration

package data

import (
	"context"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestCategoryDeletionRejectsConcurrentChildIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	repo := NewCategoryRepo(f.data, log.DefaultLogger)
	parent, err := repo.CreateCategory(ctx, 0, f.prefix+"_parent", 0)
	require.NoError(t, err)
	defer func() {
		_, err := f.pool.Exec(f.ctx, "DELETE FROM categories WHERE parent_id=$1", parent.ID)
		require.NoError(t, err)
		_, err = f.pool.Exec(f.ctx, "DELETE FROM categories WHERE id=$1", parent.ID)
		require.NoError(t, err)
	}()

	creator, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer creator.Rollback(context.Background())
	child, err := db.New(creator).CreateCategory(ctx, db.CreateCategoryParams{
		ParentID: pgtype.Int8{Int64: parent.ID, Valid: true}, Name: f.prefix + "_child",
	})
	require.NoError(t, err)

	// The DELETE snapshot cannot see the uncommitted child, so it must rely on
	// the FK after waiting for the child's parent-row KEY SHARE lock.
	deleter, err := f.pool.Acquire(ctx)
	require.NoError(t, err)
	defer deleter.Release()
	deleting := NewCategoryRepo(&Data{q: db.New(deleter)}, log.DefaultLogger)
	done := make(chan error, 1)
	go func() { done <- deleting.DeleteCategory(ctx, parent.ID) }()
	require.Eventually(t, func() bool {
		var blocked bool
		err := f.pool.QueryRow(ctx, "SELECT cardinality(pg_blocking_pids($1)) > 0",
			int32(deleter.Conn().PgConn().PID())).Scan(&blocked)
		return err == nil && blocked
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, creator.Commit(ctx))
	select {
	case err := <-done:
		require.ErrorIs(t, err, biz.ErrCategoryHasChildren)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	stored, err := f.data.DB(ctx).GetCategory(ctx, child.ID)
	require.NoError(t, err)
	require.Equal(t, parent.ID, stored.ParentID.Int64)
	require.True(t, stored.ParentID.Valid, "the child must never be silently promoted to a root")
	_, err = f.data.DB(ctx).GetCategory(ctx, parent.ID)
	require.NoError(t, err)
	require.NoError(t, repo.DeleteCategory(ctx, child.ID), "an unused leaf is still deletable")
	require.NoError(t, repo.DeleteCategory(ctx, parent.ID))
}
