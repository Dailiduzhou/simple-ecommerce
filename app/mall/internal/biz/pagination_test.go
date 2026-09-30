package biz

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPaginationBounds(t *testing.T) {
	for _, tc := range []struct {
		page, size int32
		fail       bool
	}{
		{0, 0, false}, {1, 100, false}, {2, 20, false}, {math.MaxInt32, 1, false},
		{math.MaxInt32, 100, true}, {1, 101, true}, {-1, 20, true}, {1, -1, true},
	} {
		p, err := NewPage(tc.page, tc.size)
		if tc.fail {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		require.GreaterOrEqual(t, p.Offset, int32(0))
		require.Greater(t, p.Limit, int32(0))
		require.LessOrEqual(t, p.Limit, MaxPageSize)
	}
	for _, limit := range []int32{-1, 101, math.MaxInt32} {
		_, err := NewOffsetPage(limit, 0)
		require.Error(t, err)
	}
	_, err := NewOffsetPage(20, -1)
	require.Error(t, err)
}

func TestCatalogStatusBounds(t *testing.T) {
	for _, status := range []int32{-1, 2, 65537, math.MaxInt32} {
		require.Error(t, ValidateProductStatus(status))
	}
	for _, status := range []int32{-1, 3, 65537, math.MaxInt32} {
		require.Error(t, ValidateEventStatus(status))
	}
	for _, status := range []int32{0, 1} {
		require.NoError(t, ValidateProductStatus(status))
	}
	for _, status := range []int32{0, 1, 2} {
		require.NoError(t, ValidateEventStatus(status))
	}
}
