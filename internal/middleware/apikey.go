package middleware

import (
	"crypto/subtle"
	"net/http"
)

// APIKeyHeader is the header carrying the shared API key.
const APIKeyHeader = "X-MCP-API-Key"

// APIKeyMiddleware rejects requests that do not carry the expected API key in
// the X-MCP-API-Key header. An empty key disables the check.
func APIKeyMiddleware(key string, next http.Handler) http.Handler {
	if key == "" {
		return next
	}
	expected := []byte(key)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get(APIKeyHeader))
		if subtle.ConstantTimeCompare(got, expected) != 1 {
			http.Error(w, "invalid or missing "+APIKeyHeader, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
