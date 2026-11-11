package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/stretchr/testify/require"
)

func TestCORSFollowsConfiguredOrigins(t *testing.T) {
	for _, tc := range []struct {
		name        string
		origins     string
		requestFrom string
		allowed     bool
	}{
		{name: "disabled", origins: "", requestFrom: "https://web.example.com"},
		{name: "disabled-by-blank-list", origins: " , ", requestFrom: "https://web.example.com"},
		{name: "wildcard-allows-any", origins: "*", requestFrom: "https://arbitrary.example", allowed: true},
		{name: "list-allows-listed", origins: "https://web.example.com, https://admin.example.com", requestFrom: "https://admin.example.com", allowed: true},
		{name: "list-rejects-unlisted", origins: "https://web.example.com", requestFrom: "https://evil.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newHTTPTestServerWithConfig(&callbackPaymentUsecase{}, &conf.Server{
				Http: &conf.Server_HTTP{CorsAllowedOrigins: tc.origins},
			})
			for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "CUSTOM"} {
				req := httptest.NewRequest(http.MethodOptions, "/v1/orders/1", nil)
				req.Header.Set("Origin", tc.requestFrom)
				req.Header.Set("Access-Control-Request-Method", method)
				req.Header.Set("Access-Control-Request-Headers", "authorization, content-type, x-custom")
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, req)
				if tc.allowed {
					require.Contains(t, rec.Header().Values("Vary"), "Origin")
					require.Equal(t, http.StatusNoContent, rec.Code)
					require.Equal(t, tc.requestFrom, rec.Header().Get("Access-Control-Allow-Origin"))
					require.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))
					require.Equal(t, method, rec.Header().Get("Access-Control-Allow-Methods"))
					require.Equal(t, "authorization, content-type, x-custom", rec.Header().Get("Access-Control-Allow-Headers"))
				} else {
					require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
					require.Empty(t, rec.Header().Get("Access-Control-Allow-Credentials"))
				}
			}
			// Authentication still applies; the browser can read its error.
			req := httptest.NewRequest(http.MethodGet, "/v1/orders/1", nil)
			req.Header.Set("Origin", tc.requestFrom)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			require.Equal(t, http.StatusUnauthorized, rec.Code)
			if tc.allowed {
				require.Equal(t, tc.requestFrom, rec.Header().Get("Access-Control-Allow-Origin"))
			} else {
				require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
			}
		})
	}
}

func TestCORSDoesNotSwallowOrdinaryRequests(t *testing.T) {
	srv := newHTTPTestServerWithConfig(&callbackPaymentUsecase{}, &conf.Server{
		Http: &conf.Server_HTTP{CorsAllowedOrigins: "*"},
	})
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		req := httptest.NewRequest(method, "/not-a-route", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	}
	// Successful responses also retain their normal behavior.
	req := httptest.NewRequest(http.MethodPost, "/v1/payments/alipay/notify", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "success", rec.Body.String())
	require.Equal(t, "http://localhost:5173", rec.Header().Get("Access-Control-Allow-Origin"))
}
