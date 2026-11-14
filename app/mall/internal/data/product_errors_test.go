package data

import (
	"fmt"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestProductWriteErrorMapsOnlyKnownConstraints(t *testing.T) {
	for _, tc := range []struct {
		code, constraint string
		status           int
	}{
		{"23503", "fk_product_category", 404}, {"23503", "fk_product_merchant", 404},
		{"23514", "products_name_check", 400}, {"23514", "products_description_check", 400},
		{"23514", "products_price_minor_check", 400}, {"23514", "products_discount_check", 400},
		{"23514", "products_stock_check", 400}, {"23514", "products_status_check", 400},
	} {
		t.Run(tc.constraint, func(t *testing.T) {
			cause := fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: tc.code, ConstraintName: tc.constraint, Message: "sensitive SQL"})
			err := productWriteError(cause)
			require.EqualValues(t, tc.status, errors.Code(err))
			require.NotContains(t, err.Error(), "sensitive SQL")
		})
	}
	for _, cause := range []error{fmt.Errorf("sensitive connection details"), &pgconn.PgError{Code: "23514", ConstraintName: "unknown_constraint", Message: "sensitive SQL"}, &pgconn.PgError{Code: "08006", ConstraintName: "products_stock_check"}} {
		err := productWriteError(cause)
		require.ErrorIs(t, err, cause, "keep the underlying failure for internal diagnostics")
		require.EqualValues(t, 500, errors.Code(err))
		require.Equal(t, "cannot save product", errors.FromError(err).Message)
	}
	domainError := errors.Forbidden("FORBIDDEN", "not allowed")
	require.Same(t, domainError, productWriteError(domainError))
	require.NoError(t, productWriteError(nil))
}
