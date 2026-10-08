package biz

import (
	"context"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type authorizedOrderReadRepo struct {
	orderUsecaseRepo
	reads, adminReads int
}

func (r *authorizedOrderReadRepo) GetOrder(context.Context, int64) (Order, error) {
	r.reads++
	r.adminReads++
	return r.order, nil
}
func (r *authorizedOrderReadRepo) GetOrderByUser(_ context.Context, _ int64, userID int64) (Order, error) {
	r.reads++
	if r.order.UserID != userID {
		return Order{}, ErrOrderNotFound
	}
	return r.order, nil
}
func (r *authorizedOrderReadRepo) ListOrdersByUser(context.Context, int64, int32, int32) ([]Order, error) {
	r.reads++
	return []Order{r.order}, nil
}
func (r *authorizedOrderReadRepo) ListOngoingOrdersByUser(context.Context, int64, int32, int32) ([]Order, error) {
	r.reads++
	return []Order{r.order}, nil
}

func TestOrderUsecase_ReadAuthorizationAndShippingSnapshot(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	cipher, err := phonecrypto.EncryptPhone("13800138000", []byte(secret))
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		actor   Actor
		allowed bool
	}{
		{"owner", Actor{ID: 42}, true},
		{"admin", Actor{ID: 99, Admin: true}, true},
		{"other user", Actor{ID: 99}, false},
		{"anonymous", Actor{}, false},
		{"admin without identity", Actor{Admin: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shipping := &OrderShippingSnapshot{ReceiverName: "buyer", ReceiverPhoneEncrypt: cipher, DetailAddress: "checkout address"}
			repo := &authorizedOrderReadRepo{orderUsecaseRepo: orderUsecaseRepo{order: Order{ID: 7, UserID: 42, Shipping: shipping}}}
			uc := NewConfiguredOrderUsecase(repo, nil, OrderPolicy{}, &conf.Auth{PhoneSecret: secret}, log.DefaultLogger)
			order, err := uc.GetOrder(context.Background(), 7, tc.actor)
			if tc.allowed {
				require.NoError(t, err)
				require.Equal(t, "13800138000", order.Shipping.ReceiverPhone)
				require.Equal(t, "checkout address", order.Shipping.DetailAddress)
				require.NotSame(t, shipping, order.Shipping)
			} else {
				require.Error(t, err)
				require.Nil(t, order)
			}
			if tc.actor.Admin && tc.allowed {
				require.Equal(t, 1, repo.adminReads)
			} else {
				require.Zero(t, repo.adminReads, "ordinary callers never get an unscoped read")
			}
			for _, ongoing := range []bool{false, true} {
				before := repo.reads
				orders, _, err := uc.ListOrders(context.Background(), tc.actor, &ListOrdersReq{UserID: 42, Ongoing: ongoing})
				if tc.allowed {
					require.NoError(t, err)
					require.Len(t, orders, 1)
					require.Equal(t, "13800138000", orders[0].Shipping.ReceiverPhone)
				} else {
					require.Error(t, err)
					require.Nil(t, orders)
					require.Equal(t, before, repo.reads, "reject before loading another user's page")
				}
			}
			require.Empty(t, shipping.ReceiverPhone, "plaintext never mutates the repository/cache snapshot")
		})
	}
}
