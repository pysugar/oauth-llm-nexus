package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
)

// TestHandler provides a simple endpoint to test upstream connectivity
func TestHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get a valid token
		cachedToken, err := tokenMgr.GetToken("google")
		if err != nil {
			http.Error(w, "No valid token: "+err.Error(), http.StatusUnauthorized)
			return
		}

		// Build a simple test request
		payload := map[string]interface{}{
			"project":   cachedToken.ProjectID,
			"requestId": "test-" + time.Now().Format("20060102150405"),
			"model":     "gemini-2.5-flash",
			"request": map[string]interface{}{
				"contents": []map[string]interface{}{
					{
						"role": "user",
						"parts": []map[string]interface{}{
							{"text": "Say hello in one word."},
						},
					},
				},
			},
		}

		log.Printf("🧪 Testing upstream with account: %s", cachedToken.Email)

		// Call upstream (non-streaming for simplicity)
		resp, err := upstreamClient.GenerateContent(cachedToken.AccessToken, payload)
		if err != nil {
			http.Error(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// Read and forward response
		body, _ := io.ReadAll(resp.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)

		if resp.StatusCode == http.StatusOK {
			// Parse and pretty-print
			var result map[string]interface{}
			json.Unmarshal(body, &result)

			response := map[string]interface{}{
				"status":   "success",
				"account":  cachedToken.Email,
				"project":  cachedToken.ProjectID,
				"upstream": result,
			}
			json.NewEncoder(w).Encode(response)
		} else {
			w.Write(body)
		}
	}
}

// ModelsHandler lists available models from upstream
func ModelsHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cachedToken, err := tokenMgr.GetToken("google")
		if err != nil {
			http.Error(w, "No valid token: "+err.Error(), http.StatusUnauthorized)
			return
		}

		resp, err := upstreamClient.FetchAvailableModels(cachedToken.AccessToken)
		if err != nil {
			http.Error(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", "application/json")
		io.Copy(w, resp.Body)
	}
}
