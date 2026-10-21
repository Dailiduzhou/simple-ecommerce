package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type countingRequestBody struct {
	io.Reader
	read int
}

func (b *countingRequestBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (*countingRequestBody) Close() error { return nil }

func TestAnonymousHTTPBusinessBodiesAreBoundedBeforeDecoding(t *testing.T) {
	server := newHTTPTestServer(&callbackPaymentUsecase{})
	const limit = 64 << 10
	payload := strings.Repeat(" ", 2<<20) + "{}"
	for _, path := range []string{
		"/v1/payments", "/v1/products", "/v1/categories", "/v1/events",
		"/v1/posts", "/v1/media/images/uploads", "/v1/users/register", "/v1/orders",
	} {
		for _, framing := range []string{"known-length", "unknown-length", "chunked", "understated-length"} {
			t.Run(path+"/"+framing, func(t *testing.T) {
				body := &countingRequestBody{Reader: strings.NewReader(payload)}
				r := httptest.NewRequest(http.MethodPost, path, body)
				r.Header.Set("Content-Type", "application/json")
				switch framing {
				case "known-length":
					r.ContentLength = int64(len(payload))
				case "unknown-length":
					r.ContentLength = -1
				case "chunked":
					r.ContentLength = -1
					r.TransferEncoding = []string{"chunked"}
				case "understated-length":
					r.ContentLength = 1
				}
				w := httptest.NewRecorder()
				server.ServeHTTP(w, r)
				if framing == "known-length" {
					require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
					require.Zero(t, body.read)
				} else {
					// Kratos maps decoder read errors to CODEC/400. The body
					// must still stop at the cap, before parsing or JWT auth.
					require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
					require.Contains(t, w.Body.String(), "request body too large")
					require.LessOrEqual(t, body.read, limit+1)
				}
			})
		}
	}
}

type readingCallbackUsecase struct {
	callbackPaymentUsecase
	read int
}

func (u *readingCallbackUsecase) HandleNotification(_ context.Context, _ string, r *http.Request) error {
	body, err := io.ReadAll(r.Body)
	u.read = len(body)
	return err
}

func TestPaymentCallbackHasIndependentBodyLimit(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		for _, size := range []int{96 << 10, 1 << 20, (1 << 20) + 1} {
			uc := &readingCallbackUsecase{}
			server := newHTTPTestServer(uc)
			body := &countingRequestBody{Reader: strings.NewReader(strings.Repeat("x", size))}
			r := httptest.NewRequest(http.MethodPost, "/v1/payments/alipay/notify", body)
			r.ContentLength = int64(size)
			if unknown {
				r.ContentLength = -1
				r.TransferEncoding = []string{"chunked"}
			}
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)
			if size <= 1<<20 {
				require.Equal(t, http.StatusOK, w.Code)
				require.Equal(t, "success", w.Body.String())
				require.Equal(t, size, uc.read)
			} else if unknown {
				require.Equal(t, "fail", w.Body.String(), "preserve the provider failure ack on read errors")
				require.LessOrEqual(t, body.read, (1<<20)+1)
			} else {
				require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
				require.Zero(t, body.read)
			}
		}
	}
}
