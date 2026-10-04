//go:build integration

package data

import (
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestReviewTradeAccountDeletionRetainsAuditIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, paymentID, _ := f.seedPayment(t, "success")
	var postID int64
	require.NoError(t, f.pool.QueryRow(f.ctx, `INSERT INTO posts(author_id,title,content) VALUES($1,'retained','content') RETURNING id`, f.userID).Scan(&postID))
	// Exercise the actual injected wrapper: community cleanup must roll back
	// together with a rejected account deletion.
	repo := NewCommunityUserRepo(f.data, f.tx, nil, log.DefaultLogger)
	err := repo.DeleteUser(f.ctx, f.userID)
	require.Equal(t, int32(409), errors.FromError(err).Code)
	require.Equal(t, "ACCOUNT_HAS_RETAINED_HISTORY", errors.FromError(err).Reason)
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM orders o JOIN payments p ON p.order_id=o.id WHERE o.id=$1 AND p.id=$2`, orderID, paymentID).Scan(&count))
	require.Equal(t, 1, count)
	var visible bool
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT deleted_at IS NULL FROM posts WHERE id=$1`, postID).Scan(&visible))
	require.True(t, visible)
	account, err := repo.GetAuthUser(f.ctx, f.userID)
	require.NoError(t, err)
	require.NotNil(t, account)
}
func TestReviewDatabaseRejectsUnrepresentableDiscountIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	for _, discount := range []string{"0.001", "0.999", "1.01", "0", "-0.1"} {
		_, err := f.pool.Exec(f.ctx, `UPDATE products SET discount=$2::numeric WHERE id=$1`, f.productID, discount)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr)
		require.Equal(t, "23514", pgErr.Code)
	}
	_, err := f.pool.Exec(f.ctx, `UPDATE products SET discount=0.850 WHERE id=$1`, f.productID)
	require.NoError(t, err)
}
