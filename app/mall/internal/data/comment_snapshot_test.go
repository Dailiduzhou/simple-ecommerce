package data

import (
	"context"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

type recordingTxManager struct {
	q         db.Querier
	snapshots int
	writes    int
}

func (t *recordingTxManager) InTx(ctx context.Context, fn func(context.Context) error) error {
	t.writes++
	return fn(WithQuerier(ctx, t.q, nil))
}

func (t *recordingTxManager) InTxSnapshot(ctx context.Context, fn func(context.Context) error) error {
	t.snapshots++
	return fn(WithQuerier(ctx, t.q, nil))
}

// Paginated comment reads issue more than one statement (visibility check plus
// the page query, or the root lookup plus its replies), so they must observe a
// single snapshot instead of two adjacent points in time.
func TestCommentRepo_ListReadsOneSnapshot(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)
	// No fallback querier: every statement must come from the transaction ctx.
	d := newTestData(t, nil, mr)
	tx := &recordingTxManager{q: q}
	repo := NewCommentRepo(d, tx)
	page := biz.Page{Limit: 20}

	q.EXPECT().GetVisiblePost(gomock.Any(), int64(5)).Return(db.GetVisiblePostRow{ID: 5}, nil)
	q.EXPECT().ListRootComments(gomock.Any(), gomock.Any()).Return([]db.ListRootCommentsRow{{
		ID: 1, PostID: 5, AuthorID: pgtype.Int8{Int64: 7, Valid: true}, Nickname: "author",
		Content: "first", CreatedAt: pgtype.Timestamptz{Valid: true},
	}}, nil)

	comments, next, err := repo.List(context.Background(), 5, 0, page)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	require.Equal(t, "first", comments[0].Content)
	require.Empty(t, next)
	require.Equal(t, 1, tx.snapshots, "the page must be read in a snapshot transaction")
	require.Zero(t, tx.writes, "a read must not open a writable transaction")

	// A caller that is already inside a transaction provides the snapshot; the
	// repository must neither nest nor open its own.
	q.EXPECT().GetVisiblePost(gomock.Any(), int64(5)).Return(db.GetVisiblePostRow{ID: 5}, nil)
	q.EXPECT().ListRootComments(gomock.Any(), gomock.Any()).Return(nil, nil)
	_, _, err = repo.List(WithQuerier(context.Background(), q, nil), 5, 0, page)
	require.NoError(t, err)
	require.Equal(t, 1, tx.snapshots)
	require.Zero(t, tx.writes)
}
