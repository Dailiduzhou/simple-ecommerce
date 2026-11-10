package data

import (
	"context"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func orderTestAuth() *conf.Auth { return &conf.Auth{PhoneSecret: "order-test-phone-secret"} }
func orderTestAddress(t *testing.T) db.ShippingAddress {
	t.Helper()
	cipher, err := phonecrypto.EncryptPhone("13800138000", []byte(orderTestAuth().PhoneSecret))
	require.NoError(t, err)
	return db.ShippingAddress{ID: 9, UserID: 42, ReceiverName: "Receiver", ReceiverPhoneEncrypt: cipher,
		Province: "Province", City: "City", District: "District", DetailAddress: "Detail"}
}

func TestCheckoutValidatesLockedAddressBeforeAnyWrites(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*db.ShippingAddress)
		code   int32
	}{
		{"empty encrypted phone", func(a *db.ShippingAddress) {
			var err error
			a.ReceiverPhoneEncrypt, err = phonecrypto.EncryptPhone("", []byte(orderTestAuth().PhoneSecret))
			require.NoError(t, err)
		}, 400},
		{"blank receiver", func(a *db.ShippingAddress) { a.ReceiverName = " " }, 400},
		{"blank detail", func(a *db.ShippingAddress) { a.DetailAddress = " " }, 400},
		{"missing region", func(a *db.ShippingAddress) { a.City = "" }, 400},
		{"corrupt ciphertext", func(a *db.ShippingAddress) { a.ReceiverPhoneEncrypt = "corrupt" }, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			address := orderTestAddress(t)
			tc.mutate(&address)
			gomock.InOrder(
				q.EXPECT().LockOrderIdempotency(gomock.Any(), gomock.Any()).Return(nil),
				q.EXPECT().GetOrderByUserIdempotency(gomock.Any(), gomock.Any()).Return(db.Order{}, pgx.ErrNoRows),
				q.EXPECT().LockUserForReference(gomock.Any(), int64(42)).Return(int64(42), nil),
				q.EXPECT().GetShippingAddressForSnapshot(gomock.Any(), db.GetShippingAddressForSnapshotParams{ID: 9, UserID: 42}).Return(address, nil),
			)
			mq := &orderTestMQ{}
			repo := NewOrderRepoWithJobs(&Data{q: q}, testTxManager{q: q}, mq, orderTestAuth(), log.DefaultLogger)
			_, err := repo.CreateOrder(context.Background(), biz.CreateOrderArgs{UserID: 42, AddressID: 9, IdempotencyKey: "checkout", Items: []biz.OrderItemInput{{ProductID: 3, Quantity: 1}}})
			require.Error(t, err)
			require.Equal(t, tc.code, errors.FromError(err).Code)
			require.Zero(t, mq.args.OrderID)
			// No product lock, stock mutation, order insertion, or job is permitted.
		})
	}
}
