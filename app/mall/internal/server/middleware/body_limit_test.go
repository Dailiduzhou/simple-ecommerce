package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestBodyLimitAppliesToNewRoutesAndMethods(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodGet} {
		for _, path := range []string{"/v2/future-business", "/v1/payments", "/v1/payments/alipay/notify/extra"} {
			r := httptest.NewRequest(method, path, io.NopCloser(strings.NewReader(strings.Repeat("x", requestBodyLimit+10))))
			r.ContentLength = -1
			called := false
			RequestBodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				body, err := io.ReadAll(r.Body)
				var tooLarge *http.MaxBytesError
				require.ErrorAs(t, err, &tooLarge, "%s %s", method, path)
				require.EqualValues(t, requestBodyLimit, tooLarge.Limit)
				require.Len(t, body, requestBodyLimit)
			})).ServeHTTP(httptest.NewRecorder(), r)
			require.True(t, called)
		}
	}
}

func TestRequestBodyLimitAcceptsExactBusinessLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/products", strings.NewReader(strings.Repeat("x", requestBodyLimit)))
	RequestBodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Len(t, body, requestBodyLimit)
	})).ServeHTTP(httptest.NewRecorder(), r)
}
