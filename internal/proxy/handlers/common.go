package handlers

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
)

// GetTokenFromRequest handles token selection from request headers.
// Checks for explicit X-Nexus-Account header, otherwise uses Primary/Default account.
func GetTokenFromRequest(r *http.Request, tokenMgr *token.Manager) (*token.CachedToken, error) {
	if accountHeader := r.Header.Get("X-Nexus-Account"); accountHeader != "" {
		return tokenMgr.GetTokenByIdentifier(accountHeader)
	}
	return tokenMgr.GetPrimaryOrDefaultToken()
}

// GetOrGenerateRequestID retrieves X-Request-ID from header or generates a new one.
// Format: "agent-{uuid}" if generated.
func GetOrGenerateRequestID(r *http.Request) string {
	if requestId := r.Header.Get("X-Request-ID"); requestId != "" {
		return requestId
	}
	return "agent-" + uuid.New().String()
}

// SetSSEHeaders sets standard headers for Server-Sent Events streaming.
func SetSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

// GetAccountEmail retrieves the account email from request for monitoring purposes.
// Checks for explicit X-Nexus-Account header, otherwise uses Primary/Default account.
func GetAccountEmail(r *http.Request, tokenMgr *token.Manager) string {
	if accountHeader := r.Header.Get("X-Nexus-Account"); accountHeader != "" {
		if cachedToken, err := tokenMgr.GetTokenByIdentifier(accountHeader); err == nil {
			return cachedToken.Email
		}
	} else {
		if cachedToken, err := tokenMgr.GetPrimaryOrDefaultToken(); err == nil {
			return cachedToken.Email
		}
	}
	return ""
}
