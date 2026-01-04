package middleware

import (
	"net/http"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"gorm.io/gorm"
)

// APIKeyAuth middleware validates the API key from Authorization header
func APIKeyAuth(database *gorm.DB) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Get API key from database
			expectedKey := db.GetAPIKey(database)
			if expectedKey == "" {
				// No API key configured, allow all requests (first-run scenario)
				next.ServeHTTP(w, r)
				return
			}

			// Check Authorization header (Bearer token)
			authHeader := r.Header.Get("Authorization")
			if authHeader != "" {
				if strings.HasPrefix(authHeader, "Bearer ") {
					token := strings.TrimPrefix(authHeader, "Bearer ")
					if token == expectedKey {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			// Check x-api-key header (alternative)
			apiKeyHeader := r.Header.Get("x-api-key")
			if apiKeyHeader == expectedKey {
				next.ServeHTTP(w, r)
				return
			}

			// Check x-goog-api-key header (GenAI SDK)
			googApiKey := r.Header.Get("x-goog-api-key")
			if googApiKey == expectedKey {
				next.ServeHTTP(w, r)
				return
			}

			// Check 'key' query parameter (std Google API style)
			if queryKey := r.URL.Query().Get("key"); queryKey != "" {
				if queryKey == expectedKey {
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
