package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAPIKeyMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	tests := map[string]struct {
		key      string
		header   string
		wantCode int
	}{
		"disabled when key is empty": {key: "", header: "", wantCode: http.StatusNoContent},
		"accepts the correct key":    {key: "s3cret", header: "s3cret", wantCode: http.StatusNoContent},
		"rejects a wrong key":        {key: "s3cret", header: "nope", wantCode: http.StatusUnauthorized},
		"rejects a missing key":      {key: "s3cret", header: "", wantCode: http.StatusUnauthorized},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tc.header != "" {
				req.Header.Set(APIKeyHeader, tc.header)
			}
			rec := httptest.NewRecorder()
			APIKeyMiddleware(tc.key, ok).ServeHTTP(rec, req)
			assert.Equal(t, tc.wantCode, rec.Code)
		})
	}
}
