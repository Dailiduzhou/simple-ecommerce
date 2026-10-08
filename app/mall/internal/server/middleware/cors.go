package middleware

import "net/http"

// DevelopmentCORS permits any origin, method and requested header, including
// credentialed requests. Never install this filter in production.
func DevelopmentCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		w.Header().Add("Vary", "Access-Control-Request-Method")
		w.Header().Add("Vary", "Access-Control-Request-Headers")
		if origin := r.Header.Get("Origin"); origin != "" {
			// Wildcard origins cannot be used with credentials. Echoing the
			// origin permits both cookie-based and bearer-token clients.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			if method := r.Header.Get("Access-Control-Request-Method"); r.Method == http.MethodOptions && method != "" {
				w.Header().Set("Access-Control-Allow-Methods", method)
				if headers := r.Header.Get("Access-Control-Request-Headers"); headers != "" {
					w.Header().Set("Access-Control-Allow-Headers", headers)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
