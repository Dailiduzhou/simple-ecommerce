package biz

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

// Model the shared result returned to callers of a singleflight cache miss.
type sharedOrderPageRepo struct {
	orderUsecaseRepo
	orders []Order
}

func (r *sharedOrderPageRepo) ListOrdersByUser(context.Context, int64, int32, int32) ([]Order, error) {
	return r.orders, nil
}

func (r *sharedOrderPageRepo) ListOngoingOrdersByUser(context.Context, int64, int32, int32) ([]Order, error) {
	return r.orders, nil
}

func TestOrderUsecase_ListOrdersOwnsShippingPage(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	const phone = "13800138000"
	cipher, err := phonecrypto.EncryptPhone(phone, []byte(secret))
	require.NoError(t, err)

	for _, ongoing := range []bool{false, true} {
		t.Run(fmt.Sprintf("ongoing=%t", ongoing), func(t *testing.T) {
			shipping := &OrderShippingSnapshot{ReceiverName: "Receiver", ReceiverPhoneEncrypt: cipher}
			repo := &sharedOrderPageRepo{orders: []Order{{ID: 1, UserID: 7, Shipping: shipping}}}
			uc := NewConfiguredOrderUsecase(repo, nil, OrderPolicy{}, &conf.Auth{PhoneSecret: secret}, log.DefaultLogger)
			req := &ListOrdersReq{UserID: 7, Ongoing: ongoing, Limit: 20}

			const callers = 32
			pages := make([][]Order, callers)
			errs := make([]error, callers)
			totals := make([]int64, callers)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					pages[i], totals[i], errs[i] = uc.ListOrders(context.Background(), req)
				}()
			}
			close(start)
			wg.Wait()

			require.Same(t, shipping, repo.orders[0].Shipping)
			require.Empty(t, repo.orders[0].Shipping.ReceiverPhone, "cached result must remain ciphertext-only")
			for i := range callers {
				require.NoError(t, errs[i])
				require.EqualValues(t, 1, totals[i])
				require.Len(t, pages[i], 1)
				require.NotSame(t, &repo.orders[0], &pages[i][0])
				require.NotSame(t, shipping, pages[i][0].Shipping)
				require.Equal(t, phone, pages[i][0].Shipping.ReceiverPhone)
			}
			pages[0][0].ID = 99
			pages[0][0].Shipping.ReceiverPhone = "changed"
			require.EqualValues(t, 1, repo.orders[0].ID)
			for i := 1; i < callers; i++ {
				require.EqualValues(t, 1, pages[i][0].ID)
				require.Equal(t, phone, pages[i][0].Shipping.ReceiverPhone)
			}
		})
	}
}
