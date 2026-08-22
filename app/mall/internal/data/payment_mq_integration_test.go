package data

import (
	"reflect"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

func TestCheckPayInsertOptsSeparatesTechnicalAttemptsAndActiveUniqueness(t *testing.T) {
	repo := &PaymentMQRepo{}
	opts := repo.checkPayInsertOpts(biz.CheckPayArgs{PaymentID: 10, Provider: "wechat", MaxPolls: 30}, time.Time{})
	require.Equal(t, 8, opts.MaxAttempts)
	require.Contains(t, opts.UniqueOpts.ByState, rivertype.JobStateRunning)
	require.Contains(t, opts.UniqueOpts.ByState, rivertype.JobStateScheduled)
	require.NotContains(t, opts.UniqueOpts.ByState, rivertype.JobStateCompleted)
	require.NotContains(t, opts.UniqueOpts.ByState, rivertype.JobStateDiscarded)
}

func TestCheckPayArgsUsesNotificationIDAsUniqueDimension(t *testing.T) {
	field, ok := reflect.TypeOf(biz.CheckPayArgs{}).FieldByName("NotificationID")
	require.True(t, ok)
	require.Equal(t, "unique", field.Tag.Get("river"))
}
