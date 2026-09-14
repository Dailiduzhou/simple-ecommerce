package data

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	dbmock "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

func TestCommunityJobExhaustionIsObservableWithoutPaymentPersistence(t *testing.T) {
	for _, kind := range []string{biz.HistoryCleanupKind, biz.MediaSweepKind, biz.MediaDeleteKind} {
		t.Run(kind, func(t *testing.T) {
			var out bytes.Buffer
			// Nil database/Redis deliberately catch accidental payment reconciliation.
			h := NewPaymentRiverErrorHandler(nil, nil, log.NewStdLogger(&out))
			job := &rivertype.JobRow{ID: 42, Kind: kind, Attempt: 9, MaxAttempts: 10}
			h.HandleError(context.Background(), job, errors.New("credential-must-not-appear"))
			require.Empty(t, out.String())
			job.Attempt = 10
			h.HandleError(context.Background(), job, errors.New("credential-must-not-appear"))
			require.Contains(t, out.String(), "event=river_job_discarded")
			require.Contains(t, out.String(), "kind="+kind)
			require.NotContains(t, out.String(), "credential-must-not-appear")
		})
	}
}

func TestUserRepoDeleteUsesTransactionAndDefersCacheInvalidation(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := dbmock.NewMockQuerier(ctrl)
	q.EXPECT().GetUserByID(gomock.Any(), int64(12)).Return(db.User{ID: 12, PhoneHash: "private"}, nil)
	q.EXPECT().DeleteUser(gomock.Any(), int64(12)).Return(nil)
	state := &txState{}
	ctx := context.WithValue(WithQuerier(context.Background(), q, nil), txStateKey{}, state)
	// No fallback database or Redis: either use before commit would panic.
	repo := NewUserRepo(&Data{}, log.DefaultLogger)
	require.NoError(t, repo.DeleteUser(ctx, 12))
	require.Len(t, state.afterCommit, 1)
}

func TestCommunityConfigurationRejectsUnboundedQueuesBeforeConnecting(t *testing.T) {
	_, err := NewConfiguredRiverClient(nil, nil, nil, nil, &conf.Community{MediaWorkers: 33})
	require.ErrorContains(t, err, "community.media_workers")
}
