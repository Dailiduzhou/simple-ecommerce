package middleware

import (
	"net/http"
	"strings"
)

const (
	requestBodyLimit         = 64 << 10
	paymentCallbackBodyLimit = 1 << 20 // Matches the payment adapters' notification limit.
)

// RequestBodyLimit runs before routing, decoding and authentication. New business
// routes are capped by default instead of having to opt into a path allowlist.
func RequestBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(requestBodyLimit)
		parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
		if r.Method == http.MethodPost && len(parts) == 5 &&
			parts[1] == "v1" && parts[2] == "payments" && parts[3] != "" && parts[4] == "notify" {
			limit = paymentCallbackBodyLimit
		}
		if r.ContentLength > limit {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		// Also enforce the limit for chunked bodies and missing/incorrect length
		// headers. At most limit+1 bytes can be read from the original body.
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
