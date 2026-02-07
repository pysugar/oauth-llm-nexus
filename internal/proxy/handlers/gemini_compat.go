package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/upstream/vertexkey"
)

// GeminiCompatProvider is a global provider used by Gemini-compatible v1beta endpoints.
// It is initialized at startup only when NEXUS_VERTEX_API_KEY is set.
var GeminiCompatProvider *vertexkey.Provider

// InitGeminiCompatProviderFromEnv initializes the provider from environment variables.
// Returns true when the provider is enabled.
func InitGeminiCompatProviderFromEnv() bool {
	GeminiCompatProvider = vertexkey.NewProviderFromEnv()
	return GeminiCompatProvider != nil && GeminiCompatProvider.IsEnabled()
}

// GeminiCompatProxyHandler handles:
// - POST /v1beta/models/{model}:generateContent
// - POST /v1beta/models/{model}:streamGenerateContent
// - POST /v1beta/models/{model}:countTokens
func GeminiCompatProxyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if GeminiCompatProvider == nil || !GeminiCompatProvider.IsEnabled() {
			writeGeminiCompatError(w, "Gemini compatibility proxy is not enabled", http.StatusServiceUnavailable)
			return
		}

		model, action, ok := parseGeminiCompatModelAction(r.URL.Path)
		if !ok {
			writeGeminiCompatError(w, "Unsupported endpoint", http.StatusNotFound)
			return
		}

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			writeGeminiCompatError(w, "Failed to read request body", http.StatusBadRequest)
			return
		}

		resp, err := GeminiCompatProvider.Forward(
			r.Context(),
			r.Method,
			model,
			action,
			r.URL.Query(),
			r.Header,
			bodyBytes,
		)
		if err != nil {
			log.Printf("❌ Gemini compat proxy error: model=%s action=%s err=%v", model, action, err)
			writeGeminiCompatError(w, "Upstream proxy error: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		if err := vertexkey.CopyResponse(w, resp); err != nil {
			log.Printf("❌ Gemini compat response copy error: model=%s action=%s err=%v", model, action, err)
		}
	}
}

func parseGeminiCompatModelAction(path string) (model string, action string, ok bool) {
	const prefix = "/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}

	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return "", "", false
	}

	actions := []string{"generateContent", "streamGenerateContent", "countTokens"}
	for _, candidate := range actions {
		suffix := ":" + candidate
		if strings.HasSuffix(rest, suffix) {
			model = strings.TrimSuffix(rest, suffix)
			if strings.TrimSpace(model) == "" {
				return "", "", false
			}
			return model, candidate, true
		}
	}
	return "", "", false
}

func writeGeminiCompatError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"code":    status,
		},
	})
}
