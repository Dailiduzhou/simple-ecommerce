package biz

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type orderScopeRepo struct {
	OrderRepo
	buyerID, merchantID int64
}

func (r *orderScopeRepo) ListOrdersByUser(_ context.Context, id int64, _, _ int32) ([]Order, error) {
	r.buyerID = id
	return nil, nil
}
func (r *orderScopeRepo) ListOngoingOrdersByUser(ctx context.Context, id int64, limit, offset int32) ([]Order, error) {
	return r.ListOrdersByUser(ctx, id, limit, offset)
}
func (r *orderScopeRepo) CountOrdersByUser(context.Context, int64) (int64, error) {
	return 0, nil
}
func (r *orderScopeRepo) CountOngoingOrdersByUser(context.Context, int64) (int64, error) {
	return 0, nil
}
func (r *orderScopeRepo) ListOrdersByMerchant(_ context.Context, id int64, _ bool, _, _ int32) ([]Order, error) {
	r.merchantID = id
	return nil, nil
}
func (r *orderScopeRepo) CountOrdersByMerchant(context.Context, int64, bool) (int64, error) {
	return 0, nil
}

func TestOrderListScopesPurchasesAndSalesWithoutMutatingRequest(t *testing.T) {
	for _, ongoing := range []bool{false, true} {
		for _, tc := range []struct {
			name         string
			role         Role
			userID       int64
			merchantID   int64
			wantBuyer    int64
			wantMerchant int64
			denied       bool
		}{
			{"merchant purchases", RoleMerchant, 7, 0, 7, 0, false},
			{"merchant default sales", RoleMerchant, 0, 0, 0, 7, false},
			{"merchant own sales", RoleMerchant, 0, 7, 0, 7, false},
			{"merchant cannot select foreign purchases", RoleMerchant, 8, 0, 0, 7, false},
			{"merchant cannot select foreign sales", RoleMerchant, 7, 8, 0, 7, false},
			{"buyer ignores merchant filter", RoleUser, 7, 8, 7, 0, false},
			{"buyer cannot select foreign purchases", RoleUser, 8, 0, 0, 0, true},
		} {
			t.Run(tc.name+map[bool]string{false: "/all", true: "/ongoing"}[ongoing], func(t *testing.T) {
				repo := &orderScopeRepo{}
				uc := NewOrderUsecase(repo, nil, log.DefaultLogger)
				req := &ListOrdersReq{UserID: tc.userID, MerchantID: tc.merchantID, Ongoing: ongoing, Limit: 10}
				before := *req
				_, _, err := uc.ListOrders(context.Background(), Actor{ID: 7, Role: tc.role}, req)
				if tc.denied {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, tc.wantBuyer, repo.buyerID)
				require.Equal(t, tc.wantMerchant, repo.merchantID)
				require.Equal(t, before, *req)
			})
		}
	}
}
