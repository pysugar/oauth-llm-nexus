package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
)

// RefreshHandler triggers manual token refresh
func RefreshHandler(tokenMgr *token.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Reload cache from DB (picks up newly added accounts)
		tokenMgr.ReloadAllTokens()
		
		// Trigger refresh for expiring tokens
		tokenMgr.RefreshAllTokens()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"message": "Token refresh triggered",
		})
	}
}
