package data

import (
	"context"
	"testing"

	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

func TestProductListInvalidationUsesGlobalAndCategoryGenerations(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	d := newTestData(t, q, redisServer)
	repo := NewProductRepo(d, log.DefaultLogger)
	repo.invalidateProductLists(context.Background(), 7, 8, 7)
	require.Equal(t, "1", d.rdb.Get(context.Background(), "product:list:gen").Val())
	require.Equal(t, "1", d.rdb.Get(context.Background(), "product:category:7:gen").Val())
	require.Equal(t, "1", d.rdb.Get(context.Background(), "product:category:8:gen").Val())
}
