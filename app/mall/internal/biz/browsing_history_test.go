package biz

import (
	"testing"
	"time"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/stretchr/testify/require"
)

func TestHistoryFilterValidation(t *testing.T) {
	require.NoError(t, HistoryFilter{}.Validate())
	// A single-sided window is valid, and a future end_time is harmless because
	// last_viewed_at can never exceed the server clock.
	now := time.Now().UTC()
	require.NoError(t, HistoryFilter{End: now.Add(time.Hour), HasEnd: true}.Validate())
	require.NoError(t, HistoryFilter{Start: now.Add(-time.Hour), HasStart: true}.Validate())
	start := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	require.NoError(t, HistoryFilter{Start: start, End: end, HasStart: true, HasEnd: true}.Validate())
	// Reversed and degenerate closed windows are rejected: [start, end) must be
	// a non-empty half-open interval.
	for _, f := range []HistoryFilter{
		{Start: end, End: start, HasStart: true, HasEnd: true},
		{Start: start, End: start, HasStart: true, HasEnd: true},
	} {
		require.True(t, userv1.IsInvalidTimeRange(f.Validate()), "%v", f)
	}
	// Years outside the same 1970..9999 range the cursor parser sanctions are
	// rejected so unrepresentable instants never reach PostgreSQL.
	for _, f := range []HistoryFilter{
		{Start: time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC), HasStart: true},
		{End: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), HasEnd: true},
	} {
		require.True(t, userv1.IsInvalidTimeRange(f.Validate()), "%v", f)
	}
}
