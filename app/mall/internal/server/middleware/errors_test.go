package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

func TestPublicErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   int
		reason string
	}{
		{fmt.Errorf("SQLSTATE 23503 fk_product_category"), 500, "INTERNAL"},
		{errors.BadRequest("CODEC", `parsing field id: strconv.ParseInt`), 400, "INVALID_ARGUMENT"},
		{fmt.Errorf("private wrapper: %w", errors.NotFound("PRODUCT_NOT_FOUND", "product not found")), 404, "PRODUCT_NOT_FOUND"},
		{errors.ServiceUnavailable("STORAGE_UNAVAILABLE", "storage unavailable"), 503, "STORAGE_UNAVAILABLE"},
	} {
		response := httptest.NewRecorder()
		ErrorEncoder(response, httptest.NewRequest(http.MethodGet, "/", nil), tc.err)
		require.Equal(t, tc.code, response.Code)
		require.Contains(t, response.Body.String(), tc.reason)
		for _, private := range []string{"SQLSTATE", "fk_product_category", "strconv", "private wrapper"} {
			require.NotContains(t, response.Body.String(), private)
		}
	}
	handler := SanitizeErrors(log.DefaultLogger)(func(context.Context, any) (any, error) { return nil, fmt.Errorf("driver failure") })
	_, err := handler(context.Background(), nil)
	require.Equal(t, "INTERNAL", errors.Reason(err))
}
