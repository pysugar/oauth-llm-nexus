package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"github.com/pysugar/oauth-llm-nexus/internal/providers/catalog"
)

// GatewayProviderRegistry holds the active gateway backend registry when initialized.
var GatewayProviderRegistry *provider.Registry

// SetGatewayProviderRegistry exposes the gateway backend registry to legacy /api/providers endpoints.
func SetGatewayProviderRegistry(reg *provider.Registry) {
	GatewayProviderRegistry = reg
}

func ProvidersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		seen := make(map[string]bool)
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
		for _, p := range providers {
			if id, ok := p["id"].(string); ok {
				seen[id] = true
			}
		}

		if GatewayProviderRegistry != nil {
			for _, gp := range GatewayProviderRegistry.List() {
				caps := make([]string, 0, 4)
				if gp.Endpoints.OpenAIChat != "" {
					caps = append(caps, "openai.chat")
				}
				if gp.Endpoints.OpenAIResponses != "" {
					caps = append(caps, "openai.responses")
				}
				if gp.Endpoints.Anthropic != "" {
					caps = append(caps, "anthropic.messages")
				}
				if gp.Endpoints.Gemini != "" {
					caps = append(caps, "genai")
				}
				entry := map[string]interface{}{
					"id":              gp.ID,
					"type":            "gateway",
					"display_name":    gp.DisplayName,
					"enabled":         gp.Enabled,
					"runtime_enabled": gp.Available(),
					"base_url":        gp.BaseURL(),
					"model_scope":     catalog.ModelScopeAllModels,
					"capabilities":    caps,
					"no_key":          gp.NoKey,
				}
				if seen[gp.ID] && gp.ID == "gemini" {
					// Merge gateway status onto the builtin gemini entry when available.
					for _, p := range providers {
						if p["id"] == "gemini" {
							p["enabled"] = p["enabled"].(bool) || gp.Available()
							p["runtime_enabled"] = gp.Available()
							p["base_url"] = gp.BaseURL()
						}
					}
					continue
				}
				seen[gp.ID] = true
				providers = append(providers, entry)
			}
		}

		for _, p := range catalog.GetProviders() {
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			providers = append(providers, map[string]interface{}{
				"id":              p.ID,
				"type":            "openai_compat",
				"enabled":         p.Enabled,
				"runtime_enabled": p.RuntimeEnabled,
				"base_url":        p.BaseURL,
				"model_scope":     p.ModelScope,
				"capabilities":    p.Capabilities,
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
