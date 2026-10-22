package data

import (
	"context"
	"fmt"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestCategoryDeletionMapsConcurrentChildForeignKeyConflict(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	q.EXPECT().DeleteCategoryIfUnused(gomock.Any(), int64(3)).Return(int64(0),
		fmt.Errorf("delete: %w", &pgconn.PgError{Code: "23503", ConstraintName: "fk_category_parent"}))
	d := newTestData(t, q, miniredis.RunT(t))
	repo := NewCategoryRepo(d, log.DefaultLogger)
	require.ErrorIs(t, repo.DeleteCategory(context.Background(), 3), biz.ErrCategoryHasChildren)
	require.Zero(t, d.rdb.Exists(context.Background(), categoryGenerationKey(3), categoryListGenerationKey).Val())
}

func TestCategoryDeletionPreservesUnknownDatabaseErrors(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	dbErr := &pgconn.PgError{Code: "23503", ConstraintName: "unexpected_constraint"}
	q.EXPECT().DeleteCategoryIfUnused(gomock.Any(), int64(3)).Return(int64(0), dbErr)
	repo := NewCategoryRepo(newTestData(t, q, miniredis.RunT(t)), log.DefaultLogger)
	require.ErrorIs(t, repo.DeleteCategory(context.Background(), 3), dbErr)
}
