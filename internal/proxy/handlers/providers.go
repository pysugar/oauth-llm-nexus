package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/providers/catalog"
)

func ProvidersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providers := []map[string]interface{}{
			{
				"id":           "google",
				"type":         "builtin",
				"enabled":      true,
				"capabilities": []string{"openai.chat", "openai.responses", "genai", "anthropic"},
			},
			{
				"id":           "codex",
				"type":         "builtin",
				"enabled":      CodexProvider != nil,
				"capabilities": []string{"openai.chat", "openai.responses"},
			},
			{
				"id":           "vertex",
				"type":         "builtin",
				"enabled":      VertexAIStudioProvider != nil && VertexAIStudioProvider.IsEnabled(),
				"capabilities": []string{"genai"},
			},
			{
				"id":           "gemini",
				"type":         "builtin",
				"enabled":      GeminiAIStudioProvider != nil && GeminiAIStudioProvider.IsEnabled(),
				"capabilities": []string{"genai"},
			},
		}

		for _, provider := range catalog.GetProviders() {
			providers = append(providers, map[string]interface{}{
				"id":              provider.ID,
				"type":            "openai_compat",
				"enabled":         provider.Enabled,
				"runtime_enabled": provider.RuntimeEnabled,
				"base_url":        provider.BaseURL,
				"model_scope":     provider.ModelScope,
				"capabilities":    provider.Capabilities,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"providers": providers,
		})
	}
}

func AllowedProvidersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientModel := strings.TrimSpace(r.URL.Query().Get("client_model"))
		providers := db.AllowedProvidersByClientModel(clientModel)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"client_model": clientModel,
			"providers":    providers,
		})
	}
}
