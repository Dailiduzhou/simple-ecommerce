package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeWechatPayProvider struct {
	prepay func(ctx context.Context, req *pb.PrepayJSAPIRequest) (*pb.PrepayJSAPIReply, error)
	query  func(ctx context.Context, outTradeNo string) (*pb.QueryOrderReply, error)
	close  func(ctx context.Context, outTradeNo string) (*pb.CloseOrderReply, error)
}

func (p *fakeWechatPayProvider) PrepayJSAPI(ctx context.Context, req *pb.PrepayJSAPIRequest) (*pb.PrepayJSAPIReply, error) {
	return p.prepay(ctx, req)
}

func (p *fakeWechatPayProvider) QueryOrder(ctx context.Context, outTradeNo string) (*pb.QueryOrderReply, error) {
	return p.query(ctx, outTradeNo)
}

func (p *fakeWechatPayProvider) CloseOrder(ctx context.Context, outTradeNo string) (*pb.CloseOrderReply, error) {
	return p.close(ctx, outTradeNo)
}

func TestPaymentService_WechatPayDelegatesToProvider(t *testing.T) {
	s := NewPaymentService(&fakeWechatPayProvider{
		prepay: func(ctx context.Context, req *pb.PrepayJSAPIRequest) (*pb.PrepayJSAPIReply, error) {
			assert.Equal(t, "order-1", req.OutTradeNo)
			assert.Equal(t, int32(9900), req.TotalAmount)
			return &pb.PrepayJSAPIReply{AppId: "appid", PrepayPackage: "prepay_id=wx123"}, nil
		},
		query: func(ctx context.Context, outTradeNo string) (*pb.QueryOrderReply, error) {
			assert.Equal(t, "order-1", outTradeNo)
			return &pb.QueryOrderReply{OutTradeNo: outTradeNo, TradeState: pb.TradeState_SUCCESS, TotalAmount: 9900}, nil
		},
		close: func(ctx context.Context, outTradeNo string) (*pb.CloseOrderReply, error) {
			assert.Equal(t, "order-1", outTradeNo)
			return &pb.CloseOrderReply{Success: true}, nil
		},
	})

	prepay, err := s.PrepayJSAPI(context.Background(), &pb.PrepayJSAPIRequest{
		OutTradeNo:  "order-1",
		Description: "test order",
		TotalAmount: 9900,
		Openid:      "openid-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "appid", prepay.AppId)
	assert.Equal(t, "prepay_id=wx123", prepay.PrepayPackage)

	order, err := s.QueryOrder(context.Background(), &pb.QueryOrderRequest{OutTradeNo: "order-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.TradeState_SUCCESS, order.TradeState)
	assert.Equal(t, int32(9900), order.TotalAmount)

	closed, err := s.CloseOrder(context.Background(), &pb.CloseOrderRequest{OutTradeNo: "order-1"})
	require.NoError(t, err)
	assert.True(t, closed.Success)
}

func TestPaymentService_WechatPayPropagatesProviderError(t *testing.T) {
	wantErr := errors.New("wechat failed")
	s := NewPaymentService(&fakeWechatPayProvider{
		prepay: func(ctx context.Context, req *pb.PrepayJSAPIRequest) (*pb.PrepayJSAPIReply, error) {
			return nil, wantErr
		},
	})

	got, err := s.PrepayJSAPI(context.Background(), &pb.PrepayJSAPIRequest{OutTradeNo: "order-2"})
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, got)
}

func TestPaymentService_WechatPayProviderMissing(t *testing.T) {
	s := NewPaymentService(nil)

	got, err := s.QueryOrder(context.Background(), &pb.QueryOrderRequest{OutTradeNo: "order-3"})
	require.Error(t, err)
	assert.Nil(t, got)

	se := kratoserrors.FromError(err)
	assert.Equal(t, int32(http.StatusServiceUnavailable), se.Code)
	assert.Equal(t, "WECHAT_PAY_NOT_CONFIGURED", se.Reason)
}

func TestPaymentService_HandleWechatPayNotify(t *testing.T) {
	s := NewPaymentService(nil)
	srv := khttp.NewServer()
	srv.Route("/").POST("/v1/pay/wechat/notify", s.HandleWechatPayNotify)

	req := httptest.NewRequest(http.MethodPost, "/v1/pay/wechat/notify", strings.NewReader(`{"id":"notify-1"}`))
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"code":"SUCCESS","message":"success"}`, rec.Body.String())
}
