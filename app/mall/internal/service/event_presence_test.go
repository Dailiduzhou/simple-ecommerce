package service

import (
	"context"
	"net/http/httptest"
	"testing"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	http "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
)

func TestReviewEventHTTPPresence(t *testing.T) {
	for _, query := range []string{"", "?status=0", "?status=1"} {
		t.Run(query, func(t *testing.T) {
			called := false
			r := &fakeEventRepo{listEvents: func(_ context.Context, status *int32, _, _ int32) ([]biz.Event, error) {
				called = true
				if query == "" {
					require.Nil(t, status)
				} else {
					require.NotNil(t, status)
					if query == "?status=0" {
						require.Zero(t, *status)
					} else {
						require.Equal(t, int32(1), *status)
					}
				}
				return nil, nil
			}}
			s := http.NewServer()
			pb.RegisterMallHTTPServer(s, NewMallService(nil, nil, biz.NewEventUsecase(r, log.DefaultLogger), log.DefaultLogger))
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("GET", "/v1/events"+query, nil))
			require.Equal(t, 200, w.Code)
			require.True(t, called)
		})
	}
}
