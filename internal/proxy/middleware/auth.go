package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"gorm.io/gorm"
)

// SecretEqual compares secrets in constant time.
func SecretEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// APIKeyAuth middleware validates the API key from Authorization header
func APIKeyAuth(database *gorm.DB) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Get API key from database (cached in-process; invalidated on regenerate)
			expectedKey := db.GetAPIKey(database)
			if expectedKey == "" {
				// No API key configured, allow all requests (first-run scenario)
				next.ServeHTTP(w, r)
				return
			}

			candidates := make([]string, 0, 4)
			// Authorization: Bearer <key>
			if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
				candidates = append(candidates, token)
			}
			// x-api-key (Anthropic SDK) and x-goog-api-key (GenAI SDK)
			candidates = append(candidates, r.Header.Get("x-api-key"), r.Header.Get("x-goog-api-key"))
			// 'key' query parameter (std Google API style)
			candidates = append(candidates, r.URL.Query().Get("key"))

			for _, c := range candidates {
				if c != "" && SecretEqual(c, expectedKey) {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Unauthorized
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": {"message": "Invalid API key", "type": "authentication_error"}}`))
		})
	}
}
