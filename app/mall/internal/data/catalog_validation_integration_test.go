//go:build integration

package data

import (
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestCatalogDatabaseInputConstraintsIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	for _, name := range []string{"", " \t\r\n", "\u00a0\u3000\u2009"} {
		_, err := f.pool.Exec(f.ctx, `UPDATE products SET name=$2 WHERE id=$1`, f.productID, name)
		var pg *pgconn.PgError
		require.ErrorAs(t, err, &pg)
		require.Equal(t, "products_name_check", pg.ConstraintName)
	}
	_, err := f.pool.Exec(f.ctx, `UPDATE products SET stock=-1 WHERE id=$1`, f.productID)
	var pg *pgconn.PgError
	require.ErrorAs(t, err, &pg)
	require.Equal(t, "products_stock_check", pg.ConstraintName)
	_, err = f.pool.Exec(f.ctx, `UPDATE products SET description=$2 WHERE id=$1`, f.productID, strings.Repeat("字", 10001))
	require.ErrorAs(t, err, &pg)
	require.Equal(t, "products_description_check", pg.ConstraintName)
}

func TestCatalogRepositoryMapsMissingReferencesIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	repo := NewProductRepo(f.data, f.tx, log.DefaultLogger)
	_, err := repo.CreateProduct(f.ctx, f.merchantID, 9223372036854775807, "valid", decimal.NewFromInt(1), decimal.NewFromInt(1), 0, 1, nil, nil, "")
	require.EqualValues(t, 404, errors.Code(err))
	require.NotContains(t, err.Error(), "violates")
	_, err = repo.UpdateProduct(f.ctx, f.productID, 9223372036854775807, "valid", decimal.NewFromInt(1), decimal.NewFromInt(1), nil, nil, "")
	require.EqualValues(t, 404, errors.Code(err))
	require.NotContains(t, err.Error(), "violates")
}
