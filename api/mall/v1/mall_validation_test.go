package v1

import (
	"testing"

	"buf.build/go/protovalidate"
	"github.com/stretchr/testify/require"
)

func TestCatalogRequestBounds(t *testing.T) {
	for _, status := range []int32{-1, 2, 65537} {
		require.Error(t, protovalidate.Validate(&UpdateProductStatusRequest{Id: 1, Status: status}))
	}
	for _, status := range []int32{0, 1} {
		require.NoError(t, protovalidate.Validate(&UpdateProductStatusRequest{Id: 1, Status: status}))
	}
	for _, status := range []int32{-1, 3, 65537} {
		require.Error(t, protovalidate.Validate(&UpdateEventStatusRequest{Id: 1, Status: status}))
		require.Error(t, protovalidate.Validate(&ListEventsRequest{Status: &status}))
	}
	for _, size := range []int32{-1, 101} {
		require.Error(t, protovalidate.Validate(&ListProductsRequest{PageSize: size}))
		require.Error(t, protovalidate.Validate(&ListEventsRequest{PageSize: size}))
	}
	require.NoError(t, protovalidate.Validate(&ListEventsRequest{}))
	require.NoError(t, protovalidate.Validate(&ListProductsRequest{}))
	// Content editing has no stock field, at either the wire or JSON boundary.
	descriptor := (&UpdateProductRequest{}).ProtoReflect().Descriptor()
	require.Nil(t, descriptor.Fields().ByName("stock"))
	require.True(t, descriptor.ReservedRanges().Has(6))
}
