package middleware

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/clientkey"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"gorm.io/gorm"
)

// SecretEqual compares secrets in constant time.
func SecretEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// presentedKeys returns the credentials a client may have sent, in precedence order.
func presentedKeys(r *http.Request) []string {
	candidates := make([]string, 0, 4)
	// Authorization: Bearer <key>
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		candidates = append(candidates, strings.TrimSpace(token))
	}
	// x-api-key (Anthropic SDK) and x-goog-api-key (GenAI SDK)
	candidates = append(candidates, r.Header.Get("x-api-key"), r.Header.Get("x-goog-api-key"))
	// 'key' query parameter (std Google API style)
	candidates = append(candidates, r.URL.Query().Get("key"))
	return candidates
}

// APIKeyAuth authenticates inference requests against the legacy configs.api_key (unrestricted)
// and the named client keys, storing the resolved clientkey.Caller in the request context.
// Authentication is skipped only when neither kind of key is configured (first run).
func APIKeyAuth(database *gorm.DB) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Both lookups are cached in-process and invalidated on mutation.
			legacyKey := db.GetAPIKey(database)
			hasClientKeys := clientkey.HasKeys(database)
			if legacyKey == "" && !hasClientKeys {
				next.ServeHTTP(w, r)
				return
			}

			sawDisabled := false
			for _, c := range presentedKeys(r) {
				if c == "" {
					continue
				}
				if legacyKey != "" && SecretEqual(c, legacyKey) {
					next.ServeHTTP(w, r.WithContext(clientkey.WithCaller(r.Context(), clientkey.LegacyCaller())))
					return
				}
				if !hasClientKeys {
					continue
				}
				caller, disabled, ok := clientkey.Resolve(database, c)
				if !ok {
					continue
				}
				if disabled {
					sawDisabled = true
					continue
				}
				clientkey.Touch(database, caller.ID)
				next.ServeHTTP(w, r.WithContext(clientkey.WithCaller(r.Context(), caller)))
				return
			}

			msg := "Invalid API key"
			if sawDisabled {
				msg = "API key is disabled"
			}
			writeJSONError(w, http.StatusUnauthorized, "authentication_error", msg)
		})
	}
}

// RequireProvider guards raw (non-gateway) proxy routes such as /{provider}/*, /genai and the
// Vertex publisher proxy, which bypass model routing: a restricted caller must have the served
// provider in its allowlist, and callers with a model allowlist are rejected outright because the
// upstream model cannot be checked against client aliases there.
func RequireProvider(providerOf func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller := clientkey.FromContext(r.Context())
			if caller.Restricted() {
				providerID := clientkey.NormalizeProvider(providerOf(r))
				if !caller.AllowsProvider(providerID) {
					writeJSONError(w, http.StatusForbidden, "permission_error",
						"API key "+caller.Name+" is not allowed to use provider "+providerID)
					return
				}
				if len(caller.AllowedModels) > 0 {
					writeJSONError(w, http.StatusForbidden, "permission_error",
						"API key "+caller.Name+" has a model allowlist; raw provider routes are not permitted, use the unified /v1 endpoints")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// StaticProvider returns a RequireProvider extractor for routes that always serve providerID.
func StaticProvider(providerID string) func(*http.Request) string {
	return func(*http.Request) string { return providerID }
}

func writeJSONError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"message": msg, "type": errType},
	})
}
