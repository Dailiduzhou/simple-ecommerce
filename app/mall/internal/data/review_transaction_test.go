package data

import (
	"context"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/stretchr/testify/require"
)

func TestTransactionCleanupSurvivesCancellationAndHasBoundedDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cleanup, closeCleanup := transactionCleanupContext(ctx)
	defer closeCleanup()
	require.NoError(t, cleanup.Err())
	deadline, ok := cleanup.Deadline()
	require.True(t, ok)
	require.Positive(t, time.Until(deadline))
	require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
	closeCleanup()
	require.ErrorIs(t, cleanup.Err(), context.Canceled)
}

func TestSnapshotManagerReusesExistingQuerierAndCommitState(t *testing.T) {
	state := &txState{}
	ctx := context.WithValue(WithQuerier(context.Background(), &db.Queries{}, nil), txStateKey{}, state)
	// No pool is provided: attempting an independent transaction would panic.
	tx := &transaction{}
	called := false
	require.NoError(t, tx.InTxSnapshot(ctx, func(got context.Context) error {
		require.Same(t, querierFromContext(ctx, nil), querierFromContext(got, nil))
		afterCommit(got, func() { called = true })
		return nil
	}))
	require.False(t, called, "only the outer owner may commit callbacks")
	require.Len(t, state.afterCommit, 1)
	state.afterCommit[0]()
	require.True(t, called)
}
