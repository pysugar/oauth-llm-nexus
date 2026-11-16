package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/providers/catalog"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
	"gorm.io/gorm"
)

// DashboardHandler serves the management dashboard HTML page
func DashboardHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(dashboardHTML))
	}
}

// AccountsAPIHandler returns list of accounts as JSON
func AccountsAPIHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var accounts []models.Account
		db.Find(&accounts)

		type AccountView struct {
			ID        string    `json:"id"`
			Email     string    `json:"email"`
			Provider  string    `json:"provider"`
			ExpiresAt time.Time `json:"expires_at"`
			IsActive  bool      `json:"is_active"`
			IsPrimary bool      `json:"is_primary"`
			IsValid   bool      `json:"is_valid"`
			Metadata  string    `json:"metadata"`
		}

		views := make([]AccountView, 0, len(accounts))
		for _, acc := range accounts {
			views = append(views, AccountView{
				ID:        acc.ID,
				Email:     acc.Email,
				Provider:  acc.Provider,
				ExpiresAt: acc.ExpiresAt,
				IsActive:  acc.IsActive,
				IsPrimary: acc.IsPrimary,
				IsValid:   acc.ExpiresAt.After(time.Now()),
				Metadata:  acc.Metadata,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"accounts": views,
			"count":    len(views),
		})
	}
}

// AccountModelsHandler handles GET /api/accounts/{id}/models
func AccountModelsHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountID := chi.URLParam(r, "id")

		cachedToken, err := tokenMgr.GetTokenByAccountID(accountID)
		if err != nil {
			http.Error(w, "Token not found: "+err.Error(), http.StatusUnauthorized)
			return
		}

		tokenSuffix := "..."
		if len(cachedToken.AccessToken) > 10 {
			tokenSuffix = "..." + cachedToken.AccessToken[len(cachedToken.AccessToken)-10:]
		}
		log.Printf("📊 Fetching models for Account: %s (Token: %s)", cachedToken.Email, tokenSuffix)

		resp, err := upstreamClient.FetchAvailableModels(cachedToken.AccessToken, cachedToken.ProjectID)
		if err != nil {
			http.Error(w, "Failed to fetch models: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// Read and parse response body
		var modelsData map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&modelsData); err != nil {
			http.Error(w, "Failed to parse models response", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"account_id": accountID,
			"email":      cachedToken.Email,
			"models":     modelsData,
		})
	}
}

// SetPrimaryAccountHandler handles POST /api/accounts/{id}/promote
func SetPrimaryAccountHandler(database *gorm.DB, tokenMgr *token.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		err := database.Transaction(func(tx *gorm.DB) error {
			// 1. Demote all accounts
			if err := tx.Model(&models.Account{}).Where("is_primary = ?", true).Update("is_primary", false).Error; err != nil {
				return err
			}
			// 2. Promote target account
			result := tx.Model(&models.Account{}).
				Where("id = ?", id).
				Updates(map[string]interface{}{"is_primary": true, "is_active": true})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return gorm.ErrRecordNotFound
			}
			return nil
		})

		if err != nil {
			http.Error(w, "Failed to set primary account", http.StatusInternalServerError)
			return
		}

		// Reload token cache to reflect primary change
		tokenMgr.ReloadAllTokens()

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}
}

// UpdateAccountActiveHandler updates is_active for a specific account
func UpdateAccountActiveHandler(database *gorm.DB, tokenMgr *token.Manager) http.HandlerFunc {
	type request struct {
		IsActive bool `json:"is_active"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		result := database.Model(&models.Account{}).Where("id = ?", id).Update("is_active", req.IsActive)
		if result.Error != nil {
			http.Error(w, "Failed to update account state", http.StatusInternalServerError)
			return
		}
		if result.RowsAffected == 0 {
			http.Error(w, "Account not found", http.StatusNotFound)
			return
		}

		// Reload token cache to reflect account activation changes
		tokenMgr.ReloadAllTokens()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":    "ok",
			"is_active": req.IsActive,
		})
	}
}

// RefreshAccountHandler refreshes token for a specific account
func RefreshAccountHandler(tokenMgr *token.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountID := chi.URLParam(r, "id")
		if accountID == "" {
			http.Error(w, "Account ID required", http.StatusBadRequest)
			return
		}

		if err := tokenMgr.RefreshAccountToken(accountID); err != nil {
			log.Printf("❌ Failed to refresh account %s: %v", accountID, err)
			http.Error(w, fmt.Sprintf("Refresh failed: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}
}

// GetAPIKeyHandler returns the current API key
func GetAPIKeyHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var config models.Config
		db.Where("key = ?", "api_key").First(&config)
		apiKey := config.Value
		masked := false
		if shouldMaskSensitiveData() {
			apiKey = maskAPIKey(apiKey)
			masked = true
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"api_key": apiKey,
			"masked":  masked,
		})
	}
}

// RegenerateAPIKeyHandler generates a new API key
func RegenerateAPIKeyHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keyBytes := make([]byte, 16)
		rand.Read(keyBytes)
		apiKey := "sk-" + hex.EncodeToString(keyBytes)

		db.Model(&models.Config{}).Where("key = ?", "api_key").Update("value", apiKey)
		log.Printf("🔑 Regenerated API key: %s", maskAPIKey(apiKey))

		w.Header().Set("Content-Type", "application/json")
		displayKey := apiKey
		masked := false
		if shouldMaskSensitiveData() {
			displayKey = maskAPIKey(apiKey)
			masked = true
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"api_key": displayKey,
			"masked":  masked,
		})
	}
}

func shouldMaskSensitiveData() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("NEXUS_MASK_SENSITIVE")))
	return v == "1" || v == "true" || v == "yes"
}

func maskAPIKey(apiKey string) string {
	if len(apiKey) <= 10 {
		return "***"
	}
	return apiKey[:6] + strings.Repeat("*", len(apiKey)-10) + apiKey[len(apiKey)-4:]
}

// SupportStatusHandler returns current support status for optional providers/features.
func SupportStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vertexEnabled := VertexAIStudioProvider != nil && VertexAIStudioProvider.IsEnabled()
		aiStudioEnabled := GeminiAIStudioProvider != nil && GeminiAIStudioProvider.IsEnabled()
		codexEnabled := CodexProvider != nil
		openrouterEnabled := false
		nvidiaEnabled := false
		if provider, ok := catalog.GetProvider("openrouter"); ok {
			openrouterEnabled = provider.Enabled && provider.RuntimeEnabled
		}
		if provider, ok := catalog.GetProvider("nvidia"); ok {
			nvidiaEnabled = provider.Enabled && provider.RuntimeEnabled
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"codex_enabled":                 codexEnabled,
			"vertex_ai_proxy_enabled":       vertexEnabled,
			"gemini_api_proxy_enabled":      aiStudioEnabled,
			"openrouter_proxy_enabled":      openrouterEnabled,
			"nvidia_proxy_enabled":          nvidiaEnabled,
			"gemini_vertex_proxy_enabled":   vertexEnabled, // Backward compatibility
			"gemini_aistudio_proxy_enabled": aiStudioEnabled,
		})
	}
}

var dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>OAuth-LLM-Nexus Dashboard</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <script>
        tailwind.config = {
            darkMode: 'class',
            theme: { extend: { colors: { primary: '#6366f1' } } }
        }
    </script>
    <noscript>
        <style>
            body { font-family: system-ui, sans-serif; background: #1a1a2e; color: #eee; padding: 2rem; }
            .container { max-width: 800px; margin: auto; }
        </style>
    </noscript>
    <style>
        /* Fallback styles if Tailwind fails to load */
        body:not(.bg-gray-900) { font-family: system-ui, sans-serif; background: #1a1a2e; color: #eee; padding: 2rem; }
        body:not(.bg-gray-900) .container { max-width: 800px; margin: auto; }
        body:not(.bg-gray-900)::before { content: '⚠️ Styles failed to load. Check network or visit github.com/pysugar/oauth-llm-nexus for help.'; display: block; background: #f59e0b; color: #000; padding: 0.5rem 1rem; text-align: center; font-size: 14px; }
    </style>
</head>
<body class="bg-gray-900 text-gray-100 min-h-screen">
    <div class="container mx-auto px-4 py-6 max-w-[96rem]">
        <header class="mb-6 flex justify-between items-center">
            <div>
                <h1 class="text-2xl font-bold text-white flex items-center gap-2">
                    🔐 OAuth-LLM-Nexus
                </h1>
                <p class="text-gray-400 text-sm">Multi-Protocol LLM Proxy</p>
            </div>
        <!-- User Tier is now per-account -->
        </header>

        <!-- API Endpoints Card -->
        <div class="bg-gray-800 rounded-xl p-4 mb-6">
            <div class="flex flex-col md:flex-row md:items-center md:justify-between mb-3 gap-2">
                <h3 class="text-sm font-semibold text-gray-400">🔌 API Endpoints</h3>
                <div class="flex flex-wrap items-center gap-2 text-xs">
                    <span id="support-codex" class="px-2 py-1 rounded bg-gray-700 text-gray-300">Codex: Unknown</span>
                    <span id="support-vertex" class="px-2 py-1 rounded bg-gray-700 text-gray-300">Vertex AI Proxy: Unknown</span>
                    <span id="support-aistudio" class="px-2 py-1 rounded bg-gray-700 text-gray-300">Gemini API Proxy: Unknown</span>
                    <span id="support-openrouter" class="px-2 py-1 rounded bg-gray-700 text-gray-300">OpenRouter Proxy: Unknown</span>
                    <span id="support-nvidia" class="px-2 py-1 rounded bg-gray-700 text-gray-300">NVIDIA Proxy: Unknown</span>
                </div>
            </div>
            <div class="overflow-x-auto">
                <table class="w-full text-sm">
                    <thead>
                        <tr class="text-gray-400 text-xs border-b border-gray-700">
                            <th class="text-left py-2">Protocol</th>
                            <th class="text-left py-2">Path</th>
                            <th class="text-left py-2">Model</th>
                            <th class="text-left py-2">Support</th>
                            <th class="text-right py-2">Actions</th>
                        </tr>
                    </thead>
                    <tbody id="endpoint-tests-body">
                        <tr><td colspan="5" class="text-gray-500 py-3">Loading endpoint tests...</td></tr>
                    </tbody>
                </table>
            </div>
        </div>

        <!-- API Key Card -->
        <div class="bg-gray-800 rounded-xl p-4 mb-6">
            <div class="flex justify-between items-center mb-2">
                <h3 class="text-sm font-semibold text-gray-400">🔑 API Key</h3>
                <div class="flex gap-2">
                    <button onclick="copyAPIKey()" class="text-xs bg-blue-600 hover:bg-blue-500 px-3 py-1 rounded">📋 Copy</button>
                    <button onclick="regenerateAPIKey()" class="text-xs bg-red-600 hover:bg-red-500 px-3 py-1 rounded">🔄 Regenerate</button>
                </div>
            </div>
            <div class="bg-gray-700/50 rounded p-2 font-mono text-sm">
                <span id="api-key-display" class="text-yellow-400">Loading...</span>
                <span id="api-key-masked" class="text-gray-400" style="display:none;"></span>
            </div>
            <p class="text-xs text-gray-500 mt-2">Use this key with any SDK: <code>api_key="sk-..."</code></p>
        </div>

        <!-- Test Result Panel -->
        <div id="test-panel" class="hidden bg-gray-800 rounded-xl overflow-hidden mb-6">
            <div class="px-4 py-3 border-b border-gray-700 flex justify-between items-center">
                <h2 class="font-semibold">🧪 Test Result</h2>
                <button onclick="document.getElementById('test-panel').classList.add('hidden')" class="text-gray-400 hover:text-white">✕</button>
            </div>
            <div id="test-result" class="p-3 text-xs overflow-x-auto max-h-64"></div>
        </div>

        <!-- Main Content: Linked Accounts -->
        <div class="mb-6">
            <div class="flex justify-between items-center mb-4">
                <h2 class="text-xl font-bold text-gray-200">Linked Accounts <span id="account-count" class="text-sm font-normal text-gray-500 ml-2">...</span></h2>
                <button onclick="showAddAccountModal()" class="text-xs bg-blue-600 hover:bg-blue-500 px-3 py-1 rounded">➕ Add Account</button>
            </div>
            
            <div id="accounts-container" class="space-y-4">
                <!-- Data loaded via JS -->
                <div class="text-gray-400 text-center py-12">Loading accounts data...</div>
            </div>
        </div>

        <!-- Add Account Modal -->
        <div id="add-account-modal" class="hidden fixed inset-0 bg-black/60 flex items-center justify-center z-50">
            <div class="bg-gray-800 rounded-xl p-6 max-w-sm w-full mx-4 border border-gray-700">
                <h3 class="text-lg font-bold mb-4">Add Account</h3>
                <p class="text-gray-400 text-sm mb-4">Select a provider to link:</p>
                <div class="space-y-2">
                    <a href="/auth/google/login" class="flex items-center gap-3 w-full bg-gradient-to-r from-blue-600 to-purple-600 hover:from-blue-500 hover:to-purple-500 text-white py-3 px-4 rounded-lg transition-all">
                        <span class="text-2xl">🚀</span>
                        <div class="text-left">
                            <div class="font-bold">Antigravity</div>
                            <div class="text-xs text-white/70">Google Gemini OAuth</div>
                        </div>
                    </a>
                </div>
                <button onclick="hideAddAccountModal()" class="mt-4 w-full text-gray-400 hover:text-white py-2">Cancel</button>
            </div>
        </div>

        <!-- Gateway Backends & Model Switching Card -->
        <div class="bg-gray-800 rounded-xl p-4 mb-6">
            <div class="flex flex-wrap justify-between items-center gap-2 mb-3">
                <div>
                    <h3 class="text-sm font-semibold text-gray-200">🔌 Gateway Backends, Models &amp; Protocol Compatibility <span class="text-xs text-gray-500 ml-1">(OpenAI / OpenRouter / NVIDIA / AMD / Ollama / DeepSeek / Gemini)</span></h3>
                    <p class="text-[11px] text-gray-500 mt-0.5">Click any backend's Models drawer to browse &amp; test available models, click a model to fill Switcher / 3-Protocol Tester, or switch routes at runtime.</p>
                </div>
                <div class="flex items-center gap-2">
                    <span id="gateway-status-badge" class="text-xs text-gray-400"></span>
                    <button id="gateway-clear-cooldowns-btn" onclick="clearGatewayCooldown('')" class="text-xs bg-amber-600/80 hover:bg-amber-600 px-3 py-1 rounded">🧹 Clear Cooldowns</button>
                    <button onclick="loadGatewayDashboard()" class="text-xs bg-gray-600 hover:bg-gray-500 px-3 py-1 rounded">↻ Refresh</button>
                </div>
            </div>

            <div id="gateway-providers-grid" class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3 mb-4">
                <div class="text-xs text-gray-400">Loading gateway backends...</div>
            </div>

            <!-- 3-Protocol Compatibility Tester -->
            <div id="gateway-tester-panel" class="border-t border-gray-700 pt-3 mb-4">
                <div class="flex flex-wrap justify-between items-center gap-2 mb-2">
                    <div class="text-xs font-semibold text-gray-300">🧪 3-Protocol Compatibility Tester <span class="text-gray-500 font-normal">(OpenAI Chat / OpenAI Responses / Anthropic Messages)</span></div>
                    <span class="text-[11px] text-gray-500">Tests live routing + codec translation via <code>/api/gateway/test</code></span>
                </div>
                <div class="flex flex-wrap items-center gap-2 mb-2 bg-gray-900/60 border border-gray-700/80 rounded-lg p-2.5 text-xs">
                    <input id="gateway-test-model" type="text" placeholder="Model ID (e.g. gpt-4o-mini, deepseek-chat)" class="bg-gray-800 border border-gray-600 rounded px-2 py-1 text-white w-56 focus:outline-none focus:border-blue-500">
                    <select id="gateway-test-provider" class="bg-gray-800 border border-gray-600 rounded px-2 py-1 text-white focus:outline-none focus:border-blue-500">
                        <option value="">Auto (Follow Route / Catalog)</option>
                    </select>
                    <input id="gateway-test-prompt" type="text" value="Reply briefly with OK." placeholder="Prompt" class="bg-gray-800 border border-gray-600 rounded px-2 py-1 text-white w-48 focus:outline-none focus:border-blue-500">
                    <button onclick="runGatewayThreeProtocolTests()" class="bg-emerald-600 hover:bg-emerald-500 text-white px-3 py-1 rounded font-medium">🚀 Test All 3</button>
                    <button onclick="runSingleGatewayProtocolTest('openai-chat')" class="bg-gray-700 hover:bg-gray-600 text-gray-100 px-2.5 py-1 rounded">Chat</button>
                    <button onclick="runSingleGatewayProtocolTest('openai-responses')" class="bg-gray-700 hover:bg-gray-600 text-gray-100 px-2.5 py-1 rounded">Responses</button>
                    <button onclick="runSingleGatewayProtocolTest('anthropic')" class="bg-gray-700 hover:bg-gray-600 text-gray-100 px-2.5 py-1 rounded">Messages</button>
                    <button onclick="runSingleGatewayProtocolTest('gemini')" class="bg-gray-700 hover:bg-gray-600 text-gray-100 px-2.5 py-1 rounded">Gemini</button>
                </div>
                <div id="gateway-test-results" class="bg-gray-900/50 border border-gray-700/70 rounded-lg overflow-x-auto">
                    <div class="text-xs text-gray-500 px-3 py-2">Pick any model from a backend card above (or enter a model ID) and click <strong>Test All 3</strong> to verify Chat Completions, Responses, and Anthropic Messages compatibility.</div>
                </div>
            </div>

            <div class="border-t border-gray-700 pt-3">
                <div class="flex flex-wrap justify-between items-center gap-2 mb-2">
                    <div class="flex flex-wrap items-center gap-2">
                        <div class="text-xs font-semibold text-gray-300">🔀 Model Mappings &amp; Runtime Switching <span id="gateway-model-count" class="text-gray-500 font-normal"></span></div>
                        <div id="gateway-model-tabs" class="inline-flex rounded overflow-hidden border border-gray-700 text-[11px]">
                            <button data-gateway-tab="all" class="px-2 py-0.5 bg-blue-600 text-white font-semibold">All</button>
                            <button data-gateway-tab="routes" class="px-2 py-0.5 bg-gray-800 text-gray-300 hover:bg-gray-700">Explicit Routes</button>
                            <button data-gateway-tab="ready" class="px-2 py-0.5 bg-gray-800 text-gray-300 hover:bg-gray-700">Ready Catalog</button>
                        </div>
                    </div>
                    <input id="gateway-model-filter" type="text" placeholder="Filter models or providers..." oninput="filterGatewayModels()" class="bg-gray-900 border border-gray-700 rounded px-2.5 py-1 text-xs text-white w-56 focus:outline-none focus:border-blue-500">
                </div>
                <div class="flex flex-wrap items-center gap-2 mb-3 bg-gray-900/60 border border-gray-700/80 rounded-lg p-2 text-xs">
                    <input id="gateway-quick-client" type="text" placeholder="Client model (e.g. gpt-4o)" class="bg-gray-800 border border-gray-600 rounded px-2 py-1 text-white w-44 focus:outline-none focus:border-blue-500">
                    <span class="text-gray-500">→</span>
                    <select id="gateway-quick-provider" class="bg-gray-800 border border-gray-600 rounded px-2 py-1 text-white focus:outline-none focus:border-blue-500">
                        <option value="openai">openai</option>
                    </select>
                    <input id="gateway-quick-target" type="text" placeholder="Target model (optional)" class="bg-gray-800 border border-gray-600 rounded px-2 py-1 text-white w-48 focus:outline-none focus:border-blue-500">
                    <select id="gateway-quick-strategy" class="bg-gray-800 border border-gray-600 rounded px-2 py-1 text-white focus:outline-none focus:border-blue-500">
                        <option value="">Keep policy</option>
                        <option value="failover">Failover</option>
                        <option value="pinned">Pinned</option>
                    </select>
                    <button onclick="quickSwitchGatewayBackend()" class="bg-blue-600 hover:bg-blue-500 text-white px-3 py-1 rounded font-medium">⚡ Switch</button>
                </div>
                <div id="gateway-models-container" class="max-h-96 overflow-y-auto space-y-2 text-sm">
                    <div class="text-xs text-gray-400">Loading gateway models...</div>
                </div>
            </div>
        </div>

        <!-- Model Routes Card -->
        <div class="bg-gray-800 rounded-xl p-4 mb-6">
            <div class="flex justify-between items-center mb-2">
                <h3 class="text-sm font-semibold text-gray-400">🗺️ Model Routes <span class="text-xs text-gray-500 ml-1">(Client → Provider Backend)</span></h3>
                <div class="flex gap-2">
                    <button onclick="showAddRouteModal()" class="text-xs bg-blue-600 hover:bg-blue-500 px-3 py-1 rounded">➕ Add</button>
                    <button onclick="resetModelRoutes()" class="text-xs bg-gray-600 hover:bg-gray-500 px-3 py-1 rounded">🔄 Reset</button>
                    <button onclick="loadModelRoutes()" class="text-xs bg-gray-600 hover:bg-gray-500 px-3 py-1 rounded">↻</button>
                </div>
            </div>
            <div id="routes-container" class="max-h-64 overflow-y-auto space-y-1 text-sm">
                <div class="text-gray-400">Loading routes...</div>
            </div>
        </div>

        <!-- Add/Edit Route Modal -->
        <div id="route-modal" class="hidden fixed inset-0 bg-black/60 flex items-center justify-center z-50">
            <div class="bg-gray-800 rounded-xl p-6 max-w-md w-full mx-4 border border-gray-700">
                <h3 id="route-modal-title" class="text-lg font-semibold mb-4">Add Model Route</h3>
                <form id="route-form" onsubmit="saveRoute(event)">
                    <input type="hidden" id="route-id" value="">
                    <div class="mb-3">
                        <label class="block text-sm text-gray-400 mb-1">Client Model</label>
                        <input type="text" id="route-client" class="w-full bg-gray-700 border border-gray-600 rounded px-3 py-2 text-white" placeholder="e.g., gpt-4" required>
                    </div>
                    <div class="mb-3">
                        <label class="block text-sm text-gray-400 mb-1">Backend Provider</label>
                        <select id="route-provider" class="w-full bg-gray-700 border border-gray-600 rounded px-3 py-2 text-white"></select>
                    </div>
                    <div class="mb-3">
                        <label class="block text-sm text-gray-400 mb-1">Target Model</label>
                        <input type="text" id="route-target" class="w-full bg-gray-700 border border-gray-600 rounded px-3 py-2 text-white" placeholder="e.g., gemini-3-pro-high" required>
                    </div>
                    <div class="flex justify-end gap-2 mt-4">
                        <button type="button" onclick="hideRouteModal()" class="px-4 py-2 text-gray-400 hover:text-white">Cancel</button>
                        <button type="submit" class="px-4 py-2 bg-blue-600 hover:bg-blue-500 rounded text-white">Save</button>
                    </div>
                </form>
            </div>
        </div>

        <!-- Request Monitor Card -->
        <div class="bg-gray-800 rounded-xl p-4 mb-6">
            <div class="flex justify-between items-center">
                <div class="flex items-center gap-4">
                    <h3 class="text-sm font-semibold text-gray-400">📊 Request Monitor</h3>
                    <div id="monitor-stats" class="flex gap-3 text-xs font-bold">
                        <span class="text-blue-400"><span id="stat-total">0</span> REQS</span>
                        <span class="text-green-400"><span id="stat-success">0</span> OK</span>
                        <span class="text-red-400"><span id="stat-error">0</span> ERR</span>
                    </div>
                    <button id="toggle-logging-btn" onclick="toggleLogging()" class="text-xs bg-gray-600 hover:bg-gray-500 px-3 py-1 rounded flex items-center gap-1">
                        <span id="logging-dot" class="w-2 h-2 rounded-full bg-gray-400"></span>
                        <span id="logging-label">Paused</span>
                    </button>
                </div>
                <a href="/monitor" class="text-xs bg-blue-600 hover:bg-blue-500 px-3 py-1.5 rounded text-white">Open Full Monitor →</a>
            </div>
        </div>

        <div class="mt-6 text-center py-3 border-t border-gray-700">
            <a href="/tools" class="inline-block px-4 py-2 bg-blue-600 hover:bg-blue-500 rounded text-white text-sm font-medium mr-2">🛠️ Config Inspector</a>
            <a href="/monitor" class="inline-block px-4 py-2 bg-purple-600 hover:bg-purple-500 rounded text-white text-sm font-medium mr-2">📊 Monitor</a>
            <span class="text-gray-500 text-xs"><span id="status">Ready</span> • <span class="text-gray-300 font-bold">{{VERSION}}</span> • <a href="/healthz" class="hover:text-gray-300">Health</a></span>
        </div>
    </div>

    <script>
        // Mask sensitive data: emails get masked, non-email values (e.g. masked API keys) pass through
        function maskEmail(email) {
            if (!email) return '-';
            const parts = email.split('@');
            if (parts.length !== 2) return email;
            const local = parts[0];
            const domain = parts[1];
            const maskedLocal = local.charAt(0) + '***' + (local.length > 1 ? local.charAt(local.length - 1) : '');
            const domainParts = domain.split('.');
            const maskedDomain = domainParts[0].charAt(0) + '***' + '.' + (domainParts[1] || 'com');
            return maskedLocal + '@' + maskedDomain;
        }
        
        function getTierDisplay(tierStr) {
            const tier = (tierStr || 'FREE').toUpperCase();
            if (tier.includes('ULTRA')) {
                return { tier: 'Ultra', color: 'bg-gradient-to-r from-purple-500 to-pink-500' };
            } else if (tier.includes('PRO') || tier.includes('STANDARD')) {
                // standard-tier from Cloud Code API = Gemini Code Assist Pro subscription
                return { tier: 'Pro', color: 'bg-gradient-to-r from-blue-500 to-cyan-500' };
            }
            return { tier: 'Free', color: 'bg-gray-600' };
        }

        function formatResetTime(resetTime) {
            if (!resetTime) return '';
            const diff = new Date(resetTime) - new Date();
            if (diff < 0) return 'Resetting...';
            const hours = Math.floor(diff / 3600000);
            const mins = Math.floor((diff % 3600000) / 60000);
            if (hours > 24) return Math.floor(hours/24) + 'd';
            return hours + 'h ' + mins + 'm';
        }

        async function loadAll() {
            try {
                const res = await fetch('/api/accounts', { cache: 'no-cache' });
                const data = await res.json();
                document.getElementById('account-count').textContent = data.count + ' linked';

                if (!data.accounts || data.accounts.length === 0) {
                    document.getElementById('accounts-container').innerHTML = 
                        '<div class="text-gray-400 text-center py-12 bg-gray-800 rounded-xl border border-dashed border-gray-700">' +
                        '<div class="text-4xl mb-3">👻</div><div>No accounts linked</div>' +
                        '<button onclick="showAddAccountModal()" class="mt-4 inline-block bg-blue-600 text-white px-4 py-2 rounded-lg">Add Account</button></div>';
                    return;
                }

                let html = '';
                for (const acc of data.accounts) {
                    // Parse subscription tier from account metadata
                    let storedTier = 'FREE';
                    try {
                        const meta = JSON.parse(acc.metadata || '{}');
                        storedTier = (meta.subscription_tier || 'FREE').toUpperCase();
                    } catch(e) {}
                    
                    const tier = getTierDisplay(storedTier);
                    
                    // Fetch models for this account
                    let modelsDict = null;
                    if (acc.is_active) {
                        try {
                            const mRes = await fetch('/api/accounts/' + acc.id + '/models', { cache: 'no-cache' });
                            if (mRes.ok) { 
                                const mData = await mRes.json(); 
                                modelsDict = mData.models?.models || null;
                            }
                        } catch(e) { console.error(e); }
                    }
                    html += renderAccountCard(acc, modelsDict, tier);
                }
                document.getElementById('accounts-container').innerHTML = html;
                document.getElementById('status').textContent = 'Updated: ' + new Date().toLocaleTimeString();
            } catch (err) {
                document.getElementById('accounts-container').innerHTML = '<div class="text-red-400">Error: ' + err.message + '</div>';
            }
        }

        function renderAccountCard(acc, models, tier) {
            const expiresAt = new Date(acc.expires_at);
            const isValid = expiresAt > new Date();
            const expiresIn = Math.round((expiresAt - new Date()) / 60000);
            const tokenStatus = !acc.is_active ? 'Inactive' : (isValid ? expiresIn + 'm' : 'Expired');
            const tokenColor = acc.is_active ? (isValid ? 'text-green-400' : 'text-yellow-400') : 'text-red-400';
            const accountStateBadge = acc.is_active
                ? '<span class="px-2 py-0.5 bg-green-500/20 text-green-400 rounded text-xs">Active</span>'
                : '<span class="px-2 py-0.5 bg-red-500/20 text-red-300 rounded text-xs">Inactive</span>';
            const primaryBadge = acc.is_primary ? '<span class="ml-2 px-2 py-0.5 bg-yellow-500/20 text-yellow-400 rounded text-xs">⭐ Primary</span>' : '';
            
            let html = '<div class="bg-gray-800 rounded-xl overflow-hidden border border-gray-700 mb-4">';
            // Header with provider icon
            const providerIcon = acc.provider === 'google' ? '<span title="Antigravity (Google)">🚀</span>' : '<span>🔗</span>';
            html += '<div class="px-4 py-3 flex justify-between items-center border-b border-gray-700">';
            html += '<div class="flex items-center gap-2 flex-wrap">';
            html += providerIcon + '<span class="font-medium text-white cursor-help" title="' + acc.email + '">' + maskEmail(acc.email) + '</span>' + primaryBadge;
            html += accountStateBadge;
            html += '<span class="text-xs ' + tokenColor + '">Token: ' + tokenStatus + '</span></div>';
            html += '<div class="flex items-center gap-2">';
            html += '<span class="px-2 py-1 rounded text-xs font-bold text-white ' + tier.color + '">' + tier.tier + '</span>';
            html += '<button onclick="toggleAccountActive(\'' + acc.id + '\', ' + (!acc.is_active) + ')" class="text-xs bg-gray-700 hover:bg-gray-600 px-2 py-1 rounded">' + (acc.is_active ? 'Disable' : 'Enable') + '</button>';
            html += '<button onclick="refreshAccount(\'' + acc.id + '\')" class="text-xs bg-gray-700 hover:bg-gray-600 px-2 py-1 rounded">🔄</button>';
            if (!acc.is_primary && acc.is_active) {
                html += '<button onclick="setPrimary(\'' + acc.id + '\')" class="text-xs bg-gray-700 hover:bg-gray-600 px-2 py-1 rounded">Set Primary</button>';
            }
            html += '</div></div>';
            
            // Body - Collapsible Models
            if (models && Object.keys(models).length > 0) {
                const modelEntries = Object.entries(models)
                    .filter(([k,v]) => v.displayName)
                    .sort((a,b) => (b[1].recommended ? 1 : 0) - (a[1].recommended ? 1 : 0));
                
                html += '<details class="group" open>';
                html += '<summary class="px-4 py-2 cursor-pointer text-sm text-gray-400 hover:text-gray-200 flex justify-between items-center">';
                html += '<span>📦 ' + modelEntries.length + ' Models Available</span>';
                html += '<span class="text-xs">▼</span></summary>';
                html += '<div class="p-4 pt-2">';
                html += '<div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-2">';
                
                for (const [id, m] of modelEntries) {
                    const remainingRaw = (m.quotaInfo?.remainingFraction || 0) * 100;
                    const remaining = Math.round(remainingRaw * 10) / 10; // Keep 1 decimal
                    const barColor = remaining > 50 ? 'bg-green-500' : remaining > 20 ? 'bg-yellow-500' : 'bg-red-500';
                    const resetTime = m.quotaInfo?.resetTime ? formatResetTime(m.quotaInfo.resetTime) : '';
                    
                    html += '<div class="bg-gray-700/50 rounded p-2 text-xs">';
                    html += '<div class="truncate font-medium mb-1" title="' + m.displayName + '">' + m.displayName + '</div>';
                    html += '<div class="flex items-center gap-1 mb-1"><div class="flex-1 bg-gray-600 h-1.5 rounded-full"><div class="' + barColor + ' h-1.5 rounded-full" style="width:' + remaining + '%"></div></div><span class="text-gray-400 w-8 text-right">' + remaining + '%</span></div>';
                    if (resetTime) html += '<div class="text-gray-500 text-[10px]">Reset: ' + resetTime + '</div>';
                    html += '</div>';
                }
                html += '</div></div></details>';
            } else {
                html += '<div class="p-4 text-gray-500 text-sm italic">' + (acc.is_active ? 'No models available' : 'Account inactive') + '</div>';
            }
            html += '</div>';
            return html;
        }

        async function setPrimary(id) {
            await fetch('/api/accounts/' + id + '/promote', { method: 'POST' });
            loadAll();
        }

        async function toggleAccountActive(id, nextState) {
            try {
                const res = await fetch('/api/accounts/' + id + '/active', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ is_active: nextState })
                });
                if (!res.ok) {
                    throw new Error('Failed to update account state');
                }
                await loadAll();
            } catch (e) {
                alert('Failed to update account state: ' + e.message);
            }
        }

        async function refreshAccount(id) {
            // Find button and update UI
            const btn = document.querySelector('button[onclick="refreshAccount(\'' + id + '\')"]');
            if (btn) {
                btn.disabled = true;
                btn.classList.add('opacity-50', 'cursor-not-allowed');
                btn.innerHTML = '⏳';
            }
            
            document.getElementById('status').textContent = 'Refreshing account ' + id.substring(0, 8) + '...';
            
            try {
                // Call single account refresh endpoint
                const res = await fetch('/api/accounts/' + id + '/refresh', { method: 'POST' });
                if (!res.ok) throw new Error('Refresh failed');
                
                // Reload data after successful refresh
                await loadAll();
            } catch (e) {
                alert('Failed to refresh: ' + e.message);
                await loadAll(); // Reload anyway to reset UI state
            }
        }

        async function refreshTokens() {
            // Global refresh: force refresh all tokens then reload
            document.getElementById('status').textContent = 'Refreshing all tokens...';
            const res = await fetch('/api/refresh', { method: 'POST' });
            if (res.ok) {
                document.getElementById('status').textContent = 'Tokens refreshed, reloading...';
            }
            await loadAll();
        }

        const endpointTests = [
            { id: 'openai_chat', protocol: 'OpenAI', path: '/v1/chat/completions', model: 'gemini-3-flash', supportKey: null },
            { id: 'openai_responses', protocol: 'OpenAI', path: '/v1/responses', model: 'gpt-5.2', supportKey: null },
            { id: 'anthropic_messages', protocol: 'Anthropic', path: '/anthropic/v1/messages', model: 'claude-sonnet-4-5', supportKey: null },
            { id: 'genai_generate', protocol: 'GenAI', path: '/genai/v1beta/models/{model}:generateContent', model: 'gemini-3-flash', supportKey: null },
            { id: 'vertex_generate', protocol: 'Vertex AI', path: '/v1/publishers/google/models/{model}:streamGenerateContent', model: 'gemini-3-flash-preview', supportKey: 'vertex_ai_proxy_enabled' },
            { id: 'gemini_api_generate', protocol: 'Gemini API', path: '/v1beta/models/{model}:streamGenerateContent', model: 'gemini-3-flash-preview', supportKey: 'gemini_api_proxy_enabled' },
            { id: 'openrouter_generate', protocol: 'OpenRouter', path: '/openrouter/v1/chat/completions', model: 'openrouter/free', supportKey: 'openrouter_proxy_enabled' },
            { id: 'nvidia_generate', protocol: 'NVIDIA', path: '/nvidia/v1/chat/completions', model: 'z-ai/glm4.7', supportKey: 'nvidia_proxy_enabled' }
        ];

        let supportStatus = {
            codex_enabled: false,
            vertex_ai_proxy_enabled: false,
            gemini_api_proxy_enabled: false,
            openrouter_proxy_enabled: false,
            nvidia_proxy_enabled: false
        };

        function supportChip(enabled, yesText = 'Enabled', noText = 'Disabled') {
            if (enabled) {
                return '<span class="px-2 py-0.5 rounded bg-green-500/20 text-green-400 text-xs">' + yesText + '</span>';
            }
            return '<span class="px-2 py-0.5 rounded bg-red-500/20 text-red-300 text-xs">' + noText + '</span>';
        }

        function resolveEndpointPath(item) {
            return item.path.replace('{model}', item.model);
        }

        function renderEndpointTests() {
            const body = document.getElementById('endpoint-tests-body');
            if (!body) return;

            let html = '';
            for (const item of endpointTests) {
                const path = resolveEndpointPath(item);
                const supported = item.supportKey ? !!supportStatus[item.supportKey] : true;
                const supportLabel = item.supportKey ? supportChip(supported) : supportChip(true, 'Built-in', 'Built-in');

                html += '<tr class="border-b border-gray-700/50">';
                html += '<td class="py-2 text-gray-300">' + item.protocol + '</td>';
                html += '<td class="py-2"><code class="text-blue-300">' + path + '</code></td>';
                html += '<td class="py-2"><code class="text-green-300">' + item.model + '</code></td>';
                html += '<td class="py-2">' + supportLabel + '</td>';
                html += '<td class="py-2 text-right">';
                html += '<button onclick="testEndpoint(\'' + item.id + '\')" class="text-xs bg-orange-600 hover:bg-orange-500 px-3 py-1 rounded mr-2">🧪 Test</button>';
                html += '<button onclick="copyEndpointPath(\'' + path + '\')" class="text-xs bg-gray-600 hover:bg-gray-500 px-3 py-1 rounded">📋 Copy</button>';
                html += '</td>';
                html += '</tr>';
            }
            body.innerHTML = html;
        }

        async function loadSupportStatus() {
            try {
                const res = await fetch('/api/support-status', { cache: 'no-cache' });
                if (res.ok) {
                    const data = await res.json();
                    supportStatus = {
                        codex_enabled: !!data.codex_enabled,
                        vertex_ai_proxy_enabled: !!(data.vertex_ai_proxy_enabled ?? data.gemini_vertex_proxy_enabled),
                        gemini_api_proxy_enabled: !!(data.gemini_api_proxy_enabled ?? data.gemini_aistudio_proxy_enabled),
                        openrouter_proxy_enabled: !!data.openrouter_proxy_enabled,
                        nvidia_proxy_enabled: !!data.nvidia_proxy_enabled
                    };
                }
            } catch (e) {
                console.error('Failed to load support status', e);
            }

            const codexBadge = document.getElementById('support-codex');
            const vertexBadge = document.getElementById('support-vertex');
            const aiStudioBadge = document.getElementById('support-aistudio');
            const openrouterBadge = document.getElementById('support-openrouter');
            const nvidiaBadge = document.getElementById('support-nvidia');
            if (codexBadge) {
                codexBadge.className = supportStatus.codex_enabled
                    ? 'px-2 py-1 rounded bg-green-500/20 text-green-400'
                    : 'px-2 py-1 rounded bg-red-500/20 text-red-300';
                codexBadge.textContent = 'Codex: ' + (supportStatus.codex_enabled ? 'Enabled' : 'Disabled');
            }
            if (vertexBadge) {
                vertexBadge.className = supportStatus.vertex_ai_proxy_enabled
                    ? 'px-2 py-1 rounded bg-green-500/20 text-green-400'
                    : 'px-2 py-1 rounded bg-red-500/20 text-red-300';
                vertexBadge.textContent = 'Vertex AI Proxy: ' + (supportStatus.vertex_ai_proxy_enabled ? 'Enabled' : 'Disabled');
            }
            if (aiStudioBadge) {
                aiStudioBadge.className = supportStatus.gemini_api_proxy_enabled
                    ? 'px-2 py-1 rounded bg-green-500/20 text-green-400'
                    : 'px-2 py-1 rounded bg-red-500/20 text-red-300';
                aiStudioBadge.textContent = 'Gemini API Proxy: ' + (supportStatus.gemini_api_proxy_enabled ? 'Enabled' : 'Disabled');
            }
            if (openrouterBadge) {
                openrouterBadge.className = supportStatus.openrouter_proxy_enabled
                    ? 'px-2 py-1 rounded bg-green-500/20 text-green-400'
                    : 'px-2 py-1 rounded bg-red-500/20 text-red-300';
                openrouterBadge.textContent = 'OpenRouter Proxy: ' + (supportStatus.openrouter_proxy_enabled ? 'Enabled' : 'Disabled');
            }
            if (nvidiaBadge) {
                nvidiaBadge.className = supportStatus.nvidia_proxy_enabled
                    ? 'px-2 py-1 rounded bg-green-500/20 text-green-400'
                    : 'px-2 py-1 rounded bg-red-500/20 text-red-300';
                nvidiaBadge.textContent = 'NVIDIA Proxy: ' + (supportStatus.nvidia_proxy_enabled ? 'Enabled' : 'Disabled');
            }

            renderEndpointTests();
        }

        // Clipboard helper for non-HTTPS environments
        function copyToClipboard(text, successMsg) {
            if (navigator.clipboard && window.isSecureContext) {
                navigator.clipboard.writeText(text).then(() => {
                    if (successMsg) document.getElementById('status').textContent = successMsg;
                }).catch(err => {
                    fallbackCopyText(text, successMsg);
                });
            } else {
                fallbackCopyText(text, successMsg);
            }
        }

        function fallbackCopyText(text, successMsg) {
            const textarea = document.createElement('textarea');
            textarea.value = text;
            textarea.style.position = 'fixed';
            textarea.style.opacity = '0';
            document.body.appendChild(textarea);
            textarea.focus();
            textarea.select();
            try {
                document.execCommand('copy');
                if (successMsg) document.getElementById('status').textContent = successMsg;
            } catch (err) {
                alert('Copy failed. Please copy manually: ' + text);
            }
            document.body.removeChild(textarea);
        }

        function copyEndpointPath(path) {
            const fullPath = window.location.protocol + '//' + window.location.host + path;
            copyToClipboard(fullPath, 'Copied: ' + fullPath);
        }

        function escapeInlineHtml(str) {
            return String(str || '')
                .replace(/&/g, '&amp;')
                .replace(/</g, '&lt;')
                .replace(/>/g, '&gt;')
                .replace(/"/g, '&quot;');
        }

        async function testEndpoint(endpointId) {
            document.getElementById('test-panel').classList.remove('hidden');
            document.getElementById('test-result').innerHTML = '<div class="text-gray-300">Testing ' + endpointId + '...</div>';
            try {
                const res = await fetch('/api/test?endpoint=' + encodeURIComponent(endpointId), { cache: 'no-cache' });
                const data = await res.json();
                renderTestResult(data);
            } catch (err) {
                document.getElementById('test-result').innerHTML =
                    '<div class="text-red-300">Error: ' + escapeInlineHtml(err.message) + '</div>';
            }
        }

        function renderTestResult(result) {
            const statusColor = result.skipped
                ? 'text-yellow-300'
                : (result.success ? 'text-green-400' : 'text-red-300');
            const statusText = result.skipped
                ? 'SKIPPED'
                : (result.success ? 'PASS' : 'FAIL');

            let html = '<div class="grid grid-cols-2 md:grid-cols-3 gap-3 mb-3 text-xs">';
            html += '<div class="bg-gray-700/50 rounded p-2"><div class="text-gray-400">Endpoint</div><div class="text-white font-semibold">' + escapeInlineHtml(result.endpoint) + '</div></div>';
            html += '<div class="bg-gray-700/50 rounded p-2"><div class="text-gray-400">Model</div><div class="text-green-300 font-semibold">' + escapeInlineHtml(result.model) + '</div></div>';
            html += '<div class="bg-gray-700/50 rounded p-2"><div class="text-gray-400">Status</div><div class="' + statusColor + ' font-semibold">' + statusText + ' (HTTP ' + escapeInlineHtml(result.status_code) + ')</div></div>';
            html += '<div class="bg-gray-700/50 rounded p-2"><div class="text-gray-400">Duration</div><div class="text-white">' + escapeInlineHtml(result.duration_ms) + ' ms</div></div>';
            html += '<div class="bg-gray-700/50 rounded p-2"><div class="text-gray-400">Content-Type</div><div class="text-blue-300">' + escapeInlineHtml(result.content_type || '-') + '</div></div>';
            html += '<div class="bg-gray-700/50 rounded p-2"><div class="text-gray-400">Path</div><div class="text-white truncate" title="' + escapeInlineHtml(result.path || '-') + '">' + escapeInlineHtml(result.path || '-') + '</div></div>';
            html += '</div>';

            if (result.reason) {
                html += '<div class="mb-3 text-yellow-300">Reason: ' + escapeInlineHtml(result.reason) + '</div>';
            }
            if (result.summary) {
                html += '<div class="mb-2 text-gray-300">Summary: ' + escapeInlineHtml(result.summary) + '</div>';
            }
            html += '<div class="text-gray-400 mb-1">Snippet</div>';
            html += '<pre class="bg-gray-900 rounded p-2 text-[11px] text-cyan-200 whitespace-pre-wrap break-words max-h-72 overflow-y-auto">' + escapeInlineHtml(formatSnippet(result.snippet)) + '</pre>';

            document.getElementById('test-result').innerHTML = html;
        }

        function formatSnippet(snippet) {
            const raw = String(snippet || '').trim();
            if (!raw) return '';
            try {
                return JSON.stringify(JSON.parse(raw), null, 2);
            } catch (e) {
                return raw;
            }
        }

        function showAddAccountModal() {
            document.getElementById('add-account-modal').classList.remove('hidden');
        }

        function hideAddAccountModal() {
            document.getElementById('add-account-modal').classList.add('hidden');
        }

        // API Key Functions
        let fullAPIKey = '';
        let apiKeyVisible = false;

        async function loadAPIKey() {
            try {
                const res = await fetch('/api/config/apikey');
                if (res.ok) {
                    const data = await res.json();
                    fullAPIKey = data.api_key || '';
                    if (fullAPIKey) {
                        // Always display masked version
                        const displayKey = fullAPIKey.slice(0, 7) + '****' + fullAPIKey.slice(-4);
                        document.getElementById('api-key-display').textContent = displayKey;
                        document.getElementById('api-key-display').style.display = 'inline';
                        document.getElementById('api-key-masked').style.display = 'none';
                    } else {
                        document.getElementById('api-key-display').textContent = 'Not generated';
                    }
                }
            } catch (e) { console.error(e); }
        }

        async function copyAPIKey() {
            if (fullAPIKey && fullAPIKey !== '') {
                copyToClipboard(fullAPIKey, 'API Key copied!');
                alert('API Key copied to clipboard!');
            }
        }

        async function regenerateAPIKey() {
            if (!confirm('Are you sure? This will invalidate the old key immediately.')) return;
            try {
                const res = await fetch('/api/config/apikey/regenerate', { method: 'POST' });
                if (res.ok) {
                    const data = await res.json();
                    document.getElementById('api-key-display').textContent = data.api_key;
                    alert('New API Key generated!');
                }
            } catch (e) {
                alert('Failed to regenerate key: ' + e.message);
            }
        }

        // Model Routes Functions
        function normalizeProviderForUI(provider) {
            const normalized = (provider || '').trim().toLowerCase();
            return normalized || 'google';
        }

        function providerOptionLabel(provider) {
            switch (provider) {
                case 'google':
                    return 'google (antigravity)';
                case 'gemini':
                    return 'gemini (gemini_api)';
                case 'vertex':
                    return 'vertex';
                case 'codex':
                    return 'codex';
                case 'openai':
                    return 'openai';
                case 'openrouter':
                    return 'openrouter';
                case 'nvidia':
                    return 'nvidia';
                case 'amd':
                    return 'amd (tokenfactory)';
                case 'ollama':
                    return 'ollama';
                case 'deepseek':
                    return 'deepseek';
                default:
                    return provider;
            }
        }

        function providerDisplayLabel(provider) {
            const normalized = normalizeProviderForUI(provider);
            return providerOptionLabel(normalized);
        }

        function fallbackAllowedProvidersForClientModel(clientModel) {
            const model = (clientModel || '').trim().toLowerCase();
            if (model.startsWith('gpt')) return ['codex', 'google', 'openai', 'openrouter', 'nvidia', 'amd', 'ollama', 'deepseek', 'gemini'];
            if (model.startsWith('gemini')) return ['google', 'gemini', 'vertex', 'openrouter', 'nvidia', 'amd', 'ollama', 'openai', 'deepseek'];
            if (model.startsWith('claude')) return ['google', 'openrouter', 'openai', 'nvidia', 'amd', 'ollama', 'deepseek', 'gemini'];
            return ['google', 'openai', 'openrouter', 'nvidia', 'amd', 'ollama', 'deepseek', 'gemini'];
        }

        async function fetchAllowedProvidersForClientModel(clientModel) {
            try {
                const res = await fetch('/api/providers/allowed?client_model=' + encodeURIComponent(clientModel || ''), { cache: 'no-cache' });
                if (res.ok) {
                    const data = await res.json();
                    if (Array.isArray(data.providers) && data.providers.length > 0) {
                        return data.providers.map(normalizeProviderForUI);
                    }
                }
            } catch (_) {}
            return fallbackAllowedProvidersForClientModel(clientModel);
        }

        let routeProviderOptionsRequestSeq = 0;

        async function updateRouteProviderOptions(clientModel, preferredProvider) {
            const select = document.getElementById('route-provider');
            if (!select) return;

            const reqSeq = ++routeProviderOptionsRequestSeq;
            const allowed = await fetchAllowedProvidersForClientModel(clientModel);
            if (reqSeq !== routeProviderOptionsRequestSeq) return;

            const normalizedPreferred = normalizeProviderForUI(preferredProvider);
            const selected = allowed.includes(normalizedPreferred) ? normalizedPreferred : allowed[0];

            select.innerHTML = '';
            allowed.forEach((provider) => {
                const option = document.createElement('option');
                option.value = provider;
                option.textContent = providerOptionLabel(provider);
                if (provider === selected) {
                    option.selected = true;
                }
                select.appendChild(option);
            });
        }

        async function refreshRouteProviderOptions(preferredProvider) {
            const clientInput = document.getElementById('route-client');
            const providerSelect = document.getElementById('route-provider');
            const currentProvider = (preferredProvider !== undefined)
                ? preferredProvider
                : (providerSelect ? providerSelect.value : 'google');
            await updateRouteProviderOptions(clientInput ? clientInput.value : '', currentProvider);
        }

        function initRouteProviderPolicyBindings() {
            const clientInput = document.getElementById('route-client');
            if (!clientInput) return;
            clientInput.addEventListener('input', refreshRouteProviderOptions);
            clientInput.addEventListener('change', refreshRouteProviderOptions);
        }

        async function loadModelRoutes() {
            const container = document.getElementById('routes-container');
            container.innerHTML = '<div class="text-gray-400">Loading...</div>';
            try {
                const res = await fetch('/api/model-routes', { cache: 'no-cache' });
                if (res.ok) {
                    const data = await res.json();
                    let html = '';
                    if (data.routes && data.routes.length > 0) {
                        html += '<div class="grid grid-cols-5 gap-2 text-xs text-gray-500 pb-1 border-b border-gray-700 mb-1">';
                        html += '<span>Client</span><span>Provider</span><span class="text-center">→</span><span>Target</span><span class="text-right">Actions</span></div>';
                        data.routes.forEach(r => {
                            html += '<div class="grid grid-cols-5 gap-2 items-center hover:bg-gray-700/50 rounded px-1 py-0.5 group">';
                            html += '<code class="text-blue-300 truncate text-xs">' + r.client_model + '</code>';
                            html += '<span class="text-purple-400 text-xs">' + providerDisplayLabel(r.target_provider) + '</span>';
                            html += '<span class="text-gray-500 text-center">→</span>';
                            html += '<code class="text-green-300 truncate text-xs">' + r.target_model + '</code>';
                            html += '<div class="text-right opacity-0 group-hover:opacity-100">';
                            html += '<button onclick="editRoute(' + r.id + ',\'' + r.client_model + '\',\'' + r.target_provider + '\',\'' + r.target_model + '\')" class="text-xs text-blue-400 hover:text-blue-300 mr-2">✏️</button>';
                            html += '<button onclick="deleteRoute(' + r.id + ')" class="text-xs text-red-400 hover:text-red-300">🗑️</button>';
                            html += '</div></div>';
                        });
                        html += '<div class="text-xs text-gray-500 pt-2">' + data.count + ' routes configured</div>';
                    } else {
                        html = '<div class="text-gray-500 italic">No routes configured. <a href="https://github.com/pysugar/oauth-llm-nexus/blob/main/config/model_routes.yaml" target="_blank" class="text-blue-400 hover:underline">Download template</a> or click "Reset" to load defaults.</div>';
                    }
                    container.innerHTML = html;
                }
            } catch (e) {
                container.innerHTML = '<div class="text-red-400">Failed to load routes.</div>';
            }
        }

        function showAddRouteModal() {
            document.getElementById('route-modal-title').textContent = 'Add Model Route';
            document.getElementById('route-id').value = '';
            document.getElementById('route-client').value = '';
            refreshRouteProviderOptions('google');
            document.getElementById('route-target').value = '';
            document.getElementById('route-modal').classList.remove('hidden');
        }

        function editRoute(id, client, provider, target) {
            document.getElementById('route-modal-title').textContent = 'Edit Model Route';
            document.getElementById('route-id').value = id;
            document.getElementById('route-client').value = client;
            refreshRouteProviderOptions(provider);
            document.getElementById('route-target').value = target;
            document.getElementById('route-modal').classList.remove('hidden');
        }

        function hideRouteModal() {
            document.getElementById('route-modal').classList.add('hidden');
        }

        async function saveRoute(e) {
            e.preventDefault();
            const id = document.getElementById('route-id').value;
            const client = document.getElementById('route-client').value.trim();
            const provider = document.getElementById('route-provider').value;
            const target = document.getElementById('route-target').value.trim();

            if (!client || !target) {
                alert('Client Model and Target Model are required');
                return;
            }

            const payload = {
                client_model: client,
                target_provider: provider,
                target_model: target,
                is_active: true
            };

            try {
                let res;
                if (id) {
                    // Update
                    res = await fetch('/api/model-routes/' + id, {
                        method: 'PUT',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(payload)
                    });
                } else {
                    // Create
                    res = await fetch('/api/model-routes', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(payload)
                    });
                }

                if (res.ok) {
                    hideRouteModal();
                    loadModelRoutes();
                    loadGatewayDashboard();
                } else {
                    const err = await res.json();
                    alert('Error: ' + (err.error || 'Failed to save'));
                }
            } catch (e) {
                alert('Failed: ' + e.message);
            }
        }

        async function deleteRoute(id) {
            if (!confirm('Delete this route?')) return;
            try {
                const res = await fetch('/api/model-routes/' + id, { method: 'DELETE' });
                if (res.ok) {
                    loadModelRoutes();
                    loadGatewayDashboard();
                }
            } catch (e) {
                alert('Failed to delete: ' + e.message);
            }
        }

        async function resetModelRoutes() {
            if (!confirm('Reset all model routes to default YAML configuration?')) return;
            try {
                const res = await fetch('/api/model-routes/reset', { method: 'POST' });
                if (res.ok) {
                    loadModelRoutes();
                    loadGatewayDashboard();
                    alert('Model routes reset to defaults!');
                }
            } catch (e) {
                alert('Failed to reset: ' + e.message);
            }
        }
        // ============================================
        // Request Monitor Functions
        // ============================================
        let isLoggingEnabled = false;
        let requestLogsInFlight = false;
        const REQUEST_LOGS_POLL_INTERVAL_MS = 10000;

        async function loadLoggingStatus() {
            try {
                const res = await fetch('/api/request-logs/status');
                if (res.ok) {
                    const data = await res.json();
                    isLoggingEnabled = data.enabled;
                    updateLoggingUI();
                }
            } catch (e) { console.error(e); }
        }

        function updateLoggingUI() {
            const dot = document.getElementById('logging-dot');
            const label = document.getElementById('logging-label');
            if (isLoggingEnabled) {
                dot.className = 'w-2 h-2 rounded-full bg-red-500 animate-pulse';
                label.textContent = 'Recording';
            } else {
                dot.className = 'w-2 h-2 rounded-full bg-gray-400';
                label.textContent = 'Paused';
            }
        }

        async function toggleLogging() {
            try {
                const res = await fetch('/api/request-logs/toggle', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ enabled: !isLoggingEnabled })
                });
                if (res.ok) {
                    const data = await res.json();
                    isLoggingEnabled = data.enabled;
                    updateLoggingUI();
                }
            } catch (e) { console.error(e); }
        }

        async function loadRequestLogs(force = false) {
            if (requestLogsInFlight && !force) return;
            if (document.visibilityState === 'hidden' && !force) return;

            requestLogsInFlight = true;
            try {
                const [logsRes, statsRes] = await Promise.all([
                    fetch('/api/request-logs?limit=50'),
                    fetch('/api/request-stats')
                ]);
                
                if (statsRes.ok) {
                    const stats = await statsRes.json();
                    document.getElementById('stat-total').textContent = stats.total_requests;
                    document.getElementById('stat-success').textContent = stats.success_count;
                    document.getElementById('stat-error').textContent = stats.error_count;
                }
                
                if (logsRes.ok) {
                    const data = await logsRes.json();
                    const tbody = document.getElementById('request-logs-tbody');
                    
                    // Skip if tbody doesn't exist (on main dashboard vs full monitor page)
                    if (!tbody) return;
                    
                    if (!data.logs || data.logs.length === 0) {
                        tbody.innerHTML = '<tr><td colspan="6" class="text-center py-4 text-gray-500">No logs yet. Enable logging to start recording.</td></tr>';
                        return;
                    }
                    
                    let html = '';
                    for (const log of data.logs) {
                        const statusClass = log.status >= 200 && log.status < 400 ? 'bg-green-500' : 'bg-red-500';
                        const time = new Date(log.timestamp).toLocaleTimeString();
                        const modelDisplay = log.mapped_model && log.model !== log.mapped_model 
                            ? log.model + ' → ' + log.mapped_model 
                            : (log.model || '-');
                        
                        html += '<tr class="hover:bg-gray-700/50 cursor-pointer" onclick="showRequestDetail(\'' + log.id + '\', ' + JSON.stringify(log).replace(/'/g, "\\'") + ')">';
                        html += '<td class="px-2 py-1"><span class="px-1.5 py-0.5 rounded text-white text-[10px] ' + statusClass + '">' + log.status + '</span></td>';
                        html += '<td class="px-2 py-1 font-bold">' + log.method + '</td>';
                        html += '<td class="px-2 py-1 text-blue-400 truncate max-w-[150px]">' + modelDisplay + '</td>';
                        html += '<td class="px-2 py-1 truncate max-w-[200px]">' + log.url + '</td>';
                        html += '<td class="px-2 py-1 text-right">' + log.duration + 'ms</td>';
                        html += '<td class="px-2 py-1 text-right text-gray-500">' + time + '</td>';
                        html += '</tr>';
                    }
                    tbody.innerHTML = html;
                }
            } catch (e) {
                console.error('Failed to load request logs', e);
            } finally {
                requestLogsInFlight = false;
            }
        }

        async function clearLogs() {
            if (!confirm('Clear all request logs?')) return;
            try {
                await fetch('/api/request-logs/clear', { method: 'POST' });
                loadRequestLogs(true);
            } catch (e) { console.error(e); }
        }

        function showRequestDetail(id, log) {
            const content = document.getElementById('request-detail-content');
            const statusClass = log.status >= 200 && log.status < 400 ? 'text-green-400' : 'text-red-400';
            
            let html = '<div class="grid grid-cols-2 gap-4 bg-gray-700/50 p-3 rounded">';
            html += '<div><span class="text-gray-400 text-xs">Status</span><div class="font-bold ' + statusClass + '">' + log.status + '</div></div>';
            html += '<div><span class="text-gray-400 text-xs">Duration</span><div class="font-bold">' + log.duration + 'ms</div></div>';
            html += '<div><span class="text-gray-400 text-xs">Model</span><div class="font-bold text-blue-400">' + (log.model || '-') + '</div></div>';
            html += '<div><span class="text-gray-400 text-xs">Mapped To</span><div class="font-bold text-green-400">' + (log.mapped_model || '-') + '</div></div>';
            html += '</div>';
            
            html += '<div><h4 class="text-xs font-bold text-gray-400 mb-1">📤 Request Body</h4>';
            html += '<pre class="bg-gray-900 p-2 rounded text-[10px] overflow-x-auto max-h-48">' + formatJSON(log.request_body) + '</pre></div>';
            
            html += '<div><h4 class="text-xs font-bold text-gray-400 mb-1">📥 Response Body</h4>';
            html += '<pre class="bg-gray-900 p-2 rounded text-[10px] overflow-x-auto max-h-48">' + formatJSON(log.response_body) + '</pre></div>';
            
            content.innerHTML = html;
            document.getElementById('request-detail-modal').classList.remove('hidden');
        }

        function hideRequestDetail() {
            document.getElementById('request-detail-modal').classList.add('hidden');
        }

        function formatJSON(str) {
            if (!str) return '<span class="text-gray-500 italic">empty</span>';
            try {
                return JSON.stringify(JSON.parse(str), null, 2);
            } catch (e) {
                return str.substring(0, 2000) + (str.length > 2000 ? '...' : '');
            }
        }

        // Gateway Aggregation Functions
        let gatewayProvidersCache = [];
        let gatewayModelsCache = [];
        let gatewayExpandedProviders = {};
        let gatewayProviderQueries = {};
        let gatewayModelProbes = {};
        let gatewayModelTab = 'all';
        let gatewayProtocolResults = {};

        function formatRelativeSeconds(sec) {
            const n = Number(sec || 0);
            if (!Number.isFinite(n) || n <= 0) return '0s';
            if (n < 60) return Math.ceil(n) + 's';
            const mins = Math.floor(n / 60);
            const rem = Math.ceil(n % 60);
            return mins + 'm' + (rem > 0 ? rem + 's' : '');
        }

        function primaryEndpointSummary(p) {
            const eps = p && p.endpoints ? p.endpoints : {};
            const parts = [];
            if (eps.openai_chat) parts.push('chat: ' + eps.openai_chat);
            if (eps.openai_responses && eps.openai_responses !== eps.openai_chat) parts.push('resp: ' + eps.openai_responses);
            if (eps.anthropic) parts.push('anth: ' + eps.anthropic);
            if (eps.gemini) parts.push('gem: ' + eps.gemini);
            return parts.join(' | ');
        }

        function fillGatewayModelSelection(modelId, providerId) {
            if (!modelId) return;
            const quickClient = document.getElementById('gateway-quick-client');
            const quickTarget = document.getElementById('gateway-quick-target');
            const quickProv = document.getElementById('gateway-quick-provider');
            const testModel = document.getElementById('gateway-test-model');
            const testProv = document.getElementById('gateway-test-provider');

            if (quickClient) quickClient.value = modelId;
            if (quickTarget) quickTarget.value = modelId;
            if (providerId && quickProv) quickProv.value = providerId;
            if (testModel) testModel.value = modelId;
            if (testProv) testProv.value = providerId || '';
        }

        function buildProviderModelsListHTML(p) {
            const allModels = Array.isArray(p.models) ? p.models : [];
            const query = (gatewayProviderQueries[p.id] || '').trim().toLowerCase();
            const filtered = query
                ? allModels.filter((id) => id.toLowerCase().includes(query))
                : allModels;

            if (allModels.length === 0) {
                return '<div class="text-[11px] text-gray-500 italic py-2">No discovered models yet. Check key/endpoint and click Refresh.</div>';
            }
            if (filtered.length === 0) {
                return '<div class="text-[11px] text-gray-500 italic py-2">No models matching "' + escapeInlineHtml(query) + '".</div>';
            }

            let html = '<div class="text-[10px] text-gray-400 mb-1 flex justify-between items-center">';
            html += '<span>Showing ' + filtered.length + ' / ' + allModels.length + (p.free_only ? ' (free only)' : '') + '</span>';
            html += '<span>Click ID to select · Test runs Chat</span>';
            html += '</div>';
            html += '<div class="max-h-56 overflow-y-auto space-y-1 pr-1">';
            filtered.forEach((modelId) => {
                const probeKey = p.id + '|' + modelId;
                const probe = gatewayModelProbes[probeKey];
                let dotClass = 'bg-gray-600';
                let metaHTML = '';
                if (probe) {
                    if (probe.state === 'wait') {
                        dotClass = 'bg-amber-400 animate-pulse';
                        metaHTML = '<span class="text-[10px] text-amber-300">testing...</span>';
                    } else if (probe.state === 'ok') {
                        dotClass = 'bg-green-400';
                        metaHTML = '<span class="text-[10px] text-green-300 font-mono">' + Number(probe.ms || 0) + 'ms</span>';
                    } else {
                        dotClass = 'bg-red-400';
                        metaHTML = '<span class="text-[10px] text-red-300 truncate max-w-[110px]" title="' + escapeInlineHtml(probe.summary || 'failed') + '">HTTP ' + Number(probe.status || 0) + '</span>';
                    }
                }

                html += '<div class="flex items-center justify-between gap-1.5 bg-gray-900/70 hover:bg-gray-900 border border-gray-700/70 rounded px-2 py-1">';
                html += '<div class="flex items-center gap-1.5 min-w-0 flex-1">';
                html += '<span class="w-2 h-2 rounded-full flex-shrink-0 ' + dotClass + '"></span>';
                html += '<button data-gateway-pick-model="' + escapeInlineHtml(modelId) + '" data-gateway-pick-provider="' + escapeInlineHtml(p.id) + '" title="Fill Switcher & 3-Protocol Tester" class="text-left text-[11px] font-mono text-blue-300 hover:underline truncate">' + escapeInlineHtml(modelId) + '</button>';
                html += '</div>';
                html += '<div class="flex items-center gap-1.5 flex-shrink-0">';
                html += metaHTML;
                html += '<button data-gateway-probe-model="' + escapeInlineHtml(modelId) + '" data-gateway-probe-provider="' + escapeInlineHtml(p.id) + '" class="text-[10px] bg-gray-700 hover:bg-emerald-600 text-gray-200 hover:text-white px-1.5 py-0.5 rounded">Test</button>';
                html += '</div>';
                html += '</div>';
            });
            html += '</div>';
            return html;
        }

        function refreshProviderModelsDrawer(providerId) {
            const p = gatewayProvidersCache.find((item) => item.id === providerId);
            const body = document.getElementById('gateway-prov-models-' + providerId);
            if (!p || !body) return;
            body.innerHTML = buildProviderModelsListHTML(p);
        }

        function renderGatewayProviders(providers) {
            const grid = document.getElementById('gateway-providers-grid');
            if (!grid) return;
            if (!Array.isArray(providers) || providers.length === 0) {
                grid.innerHTML = '<div class="col-span-full text-xs text-gray-500">No gateway providers configured.</div>';
                return;
            }

            let html = '';
            providers.forEach((p) => {
                const cd = p.cooldown && p.cooldown.cooling_down ? p.cooldown : null;
                let badgeClass = 'bg-gray-700 text-gray-300';
                let badgeText = 'No Key';
                if (!p.enabled) {
                    badgeClass = 'bg-gray-700 text-gray-400';
                    badgeText = 'Disabled';
                } else if (cd) {
                    badgeClass = 'bg-red-900/60 text-red-200 border border-red-700/70';
                    badgeText = 'Cooldown ' + formatRelativeSeconds(cd.remaining_seconds);
                } else if (p.available) {
                    badgeClass = 'bg-green-900/50 text-green-300 border border-green-700/60';
                    badgeText = p.no_key ? 'Ready (No Key)' : 'Ready';
                }

                const nativeList = Array.isArray(p.native) && p.native.length > 0 ? p.native.join(', ') : (p.prefer || '-');
                const keysText = p.no_key
                    ? 'no auth'
                    : (p.key_count > 0 ? p.key_count + ' key' + (p.key_count > 1 ? 's' : '') : '0 keys');
                const maskedTitle = Array.isArray(p.masked_keys) && p.masked_keys.length > 0
                    ? ' title="' + escapeInlineHtml(p.masked_keys.join(', ')) + '"'
                    : '';
                const expanded = !!gatewayExpandedProviders[p.id];
                const colSpanClass = expanded ? 'sm:col-span-2 lg:col-span-2' : '';
                const epSummary = primaryEndpointSummary(p);

                html += '<div class="bg-gray-700/40 border border-gray-700 rounded-lg p-3 flex flex-col justify-between gap-2 ' + colSpanClass + '">';
                html += '<div>';
                html += '<div class="flex items-start justify-between gap-2">';
                html += '<div class="min-w-0">';
                html += '<div class="text-xs font-semibold text-white truncate">' + escapeInlineHtml(p.display_name || p.id) + '</div>';
                html += '<div class="text-[11px] text-gray-400 font-mono truncate">' + escapeInlineHtml(p.id) + ' · ' + escapeInlineHtml(p.prefer || '-') + '</div>';
                html += '</div>';
                html += '<span class="text-[11px] px-2 py-0.5 rounded whitespace-nowrap ' + badgeClass + '">' + escapeInlineHtml(badgeText) + '</span>';
                html += '</div>';

                html += '<div class="mt-2 flex flex-wrap items-center gap-1.5 text-[11px] text-gray-400">';
                html += '<span class="bg-gray-800/80 px-1.5 py-0.5 rounded"' + maskedTitle + '>🔑 ' + escapeInlineHtml(keysText) + '</span>';
                html += '<span class="bg-gray-800/80 px-1.5 py-0.5 rounded">📦 ' + Number(p.model_count || 0) + ' models</span>';
                if (p.free_only) {
                    html += '<span class="bg-emerald-900/50 text-emerald-300 border border-emerald-700/50 px-1.5 py-0.5 rounded">Free Only</span>';
                }
                html += '</div>';
                html += '<div class="mt-1 text-[10px] text-gray-500 truncate" title="' + escapeInlineHtml(nativeList) + '">native: ' + escapeInlineHtml(nativeList) + '</div>';
                if (epSummary) {
                    html += '<div class="mt-0.5 text-[10px] text-gray-500 font-mono truncate" title="' + escapeInlineHtml(epSummary) + '">' + escapeInlineHtml(epSummary) + '</div>';
                }

                if (cd && cd.reason) {
                    html += '<div class="mt-1 text-[10px] text-red-300 truncate" title="' + escapeInlineHtml(cd.reason) + '">⚠️ ' + escapeInlineHtml(cd.reason) + ' (x' + Number(cd.failures || 1) + ')</div>';
                } else if (p.catalog_error) {
                    html += '<div class="mt-1 text-[10px] text-yellow-300 truncate" title="' + escapeInlineHtml(p.catalog_error) + '">⚠️ ' + escapeInlineHtml(p.catalog_error) + '</div>';
                }

                if (expanded) {
                    html += '<div class="mt-2 pt-2 border-t border-gray-700/80">';
                    html += '<input type="text" data-gateway-provider-search="' + escapeInlineHtml(p.id) + '" value="' + escapeInlineHtml(gatewayProviderQueries[p.id] || '') + '" placeholder="Filter ' + escapeInlineHtml(p.id) + ' models..." class="w-full bg-gray-900 border border-gray-600 rounded px-2 py-1 text-[11px] text-white mb-1.5 focus:outline-none focus:border-blue-500">';
                    html += '<div id="gateway-prov-models-' + escapeInlineHtml(p.id) + '">' + buildProviderModelsListHTML(p) + '</div>';
                    html += '</div>';
                }
                html += '</div>';

                html += '<div class="flex flex-wrap items-center justify-between gap-1.5 pt-1 border-t border-gray-700/70">';
                html += '<div class="flex items-center gap-1.5">';
                html += '<button data-gateway-toggle-models="' + escapeInlineHtml(p.id) + '" class="text-[11px] ' + (expanded ? 'bg-blue-600 text-white' : 'bg-gray-600 hover:bg-gray-500 text-gray-100') + ' px-2 py-0.5 rounded">' + (expanded ? '▾ Hide Models' : '▸ Models (' + Number(p.model_count || 0) + ')') + '</button>';
                html += '<button data-gateway-refresh-provider="' + escapeInlineHtml(p.id) + '" class="text-[11px] bg-gray-700 hover:bg-gray-600 text-gray-200 px-2 py-0.5 rounded">↻</button>';
                html += '</div>';
                if (cd) {
                    html += '<button data-gateway-clear-provider="' + escapeInlineHtml(p.id) + '" class="text-[11px] bg-amber-600/80 hover:bg-amber-600 text-white px-2 py-0.5 rounded">Clear CD</button>';
                } else {
                    html += '<span class="text-[10px] text-gray-500">' + (p.catalog_fetched_at ? 'synced' : '') + '</span>';
                }
                html += '</div>';
                html += '</div>';
            });
            grid.innerHTML = html;
        }

        function setGatewayModelTab(tab) {
            gatewayModelTab = tab || 'all';
            const tabsEl = document.getElementById('gateway-model-tabs');
            if (tabsEl) {
                tabsEl.querySelectorAll('[data-gateway-tab]').forEach((btn) => {
                    const isActive = btn.getAttribute('data-gateway-tab') === gatewayModelTab;
                    btn.className = 'px-2 py-0.5 ' + (isActive ? 'bg-blue-600 text-white font-semibold' : 'bg-gray-800 text-gray-300 hover:bg-gray-700');
                });
            }
            renderGatewayModels(gatewayModelsCache);
        }

        function classifyGatewayModel(m) {
            const cands = Array.isArray(m && m.candidates) ? m.candidates : [];
            const hasExplicitDB = cands.some((c) => c.source === 'db' && !c.legacy);
            const hasCatalog = cands.some((c) => c.source === 'catalog');
            const hasReady = cands.some((c) => c.available);
            const isLegacyOnly = cands.length > 0 && cands.every((c) => c.legacy);
            return { hasExplicitDB, hasCatalog, hasReady, isLegacyOnly };
        }

        function filterGatewayModels() {
            renderGatewayModels(gatewayModelsCache);
        }

        function renderGatewayModels(models) {
            const container = document.getElementById('gateway-models-container');
            const countEl = document.getElementById('gateway-model-count');
            const filterEl = document.getElementById('gateway-model-filter');
            if (!container) return;

            const query = filterEl ? filterEl.value.trim().toLowerCase() : '';
            const allModels = Array.isArray(models) ? models.slice() : [];

            allModels.sort((a, b) => {
                const ca = classifyGatewayModel(a);
                const cb = classifyGatewayModel(b);
                if (ca.hasExplicitDB !== cb.hasExplicitDB) return ca.hasExplicitDB ? -1 : 1;
                if (ca.hasReady !== cb.hasReady) return ca.hasReady ? -1 : 1;
                return (a.client_model || '').localeCompare(b.client_model || '');
            });

            let routeCount = 0;
            let readyCount = 0;
            allModels.forEach((m) => {
                const cls = classifyGatewayModel(m);
                if (cls.hasExplicitDB) routeCount++;
                if (cls.hasReady) readyCount++;
            });

            const tabsEl = document.getElementById('gateway-model-tabs');
            if (tabsEl) {
                const btnAll = tabsEl.querySelector('[data-gateway-tab="all"]');
                const btnRoutes = tabsEl.querySelector('[data-gateway-tab="routes"]');
                const btnReady = tabsEl.querySelector('[data-gateway-tab="ready"]');
                if (btnAll) btnAll.textContent = 'All (' + allModels.length + ')';
                if (btnRoutes) btnRoutes.textContent = 'Explicit Routes (' + routeCount + ')';
                if (btnReady) btnReady.textContent = 'Ready Catalog (' + readyCount + ')';
            }

            const tabFiltered = allModels.filter((m) => {
                const cls = classifyGatewayModel(m);
                if (gatewayModelTab === 'routes') return cls.hasExplicitDB;
                if (gatewayModelTab === 'ready') return cls.hasReady;
                return true;
            });

            const filtered = query
                ? tabFiltered.filter((m) => {
                    if ((m.client_model || '').toLowerCase().includes(query)) return true;
                    if ((m.active_provider || '').toLowerCase().includes(query)) return true;
                    return Array.isArray(m.candidates) && m.candidates.some((c) =>
                        (c.provider || '').toLowerCase().includes(query) ||
                        (c.target_model || '').toLowerCase().includes(query)
                    );
                })
                : tabFiltered;

            if (countEl) {
                countEl.textContent = '(' + filtered.length + '/' + allModels.length + ')';
            }

            if (filtered.length === 0) {
                container.innerHTML = '<div class="text-xs text-gray-500 italic py-2">No matching gateway models in this view. Switch tab above or use Quick Switch to create a model route.</div>';
                return;
            }

            let html = '';
            filtered.forEach((m) => {
                const strategy = (m.strategy || 'failover').toLowerCase();
                const isPinned = strategy === 'pinned';
                const activeProvider = m.active_provider || '';
                const cls = classifyGatewayModel(m);
                let kindBadge = '<span class="text-[10px] bg-gray-800 text-gray-400 px-1.5 py-0.5 rounded">Catalog</span>';
                if (cls.hasExplicitDB) {
                    kindBadge = '<span class="text-[10px] bg-indigo-900/60 text-indigo-300 border border-indigo-700/50 px-1.5 py-0.5 rounded">Explicit Route</span>';
                } else if (cls.isLegacyOnly) {
                    kindBadge = '<span class="text-[10px] bg-gray-800 text-amber-300/80 px-1.5 py-0.5 rounded">Legacy OAuth</span>';
                }

                html += '<div class="bg-gray-700/35 border border-gray-700/80 rounded-lg p-2.5">';
                html += '<div class="flex flex-wrap items-center justify-between gap-2 mb-2">';
                html += '<div class="flex flex-wrap items-center gap-2 min-w-0">';
                html += '<code data-gateway-fill-model="' + escapeInlineHtml(m.client_model) + '" data-gateway-fill-provider="' + escapeInlineHtml(activeProvider) + '" title="Click to fill Switcher & Tester" class="cursor-pointer hover:underline text-xs font-semibold text-blue-300 truncate">' + escapeInlineHtml(m.client_model) + '</code>';
                html += kindBadge;
                if (activeProvider) {
                    html += '<span class="text-[11px] bg-blue-900/50 text-blue-200 border border-blue-700/60 px-2 py-0.5 rounded">active: ' + escapeInlineHtml(activeProvider) + '</span>';
                } else {
                    html += '<span class="text-[11px] bg-gray-800 text-gray-400 px-2 py-0.5 rounded">no active backend</span>';
                }
                html += '</div>';

                html += '<div class="flex items-center gap-2">';
                html += '<button data-gateway-test-all="' + escapeInlineHtml(m.client_model) + '" data-gateway-test-prov="' + escapeInlineHtml(activeProvider) + '" class="text-[10px] bg-emerald-700/80 hover:bg-emerald-600 text-white px-2 py-0.5 rounded">🧪 Test 3 APIs</button>';
                html += '<div class="inline-flex rounded overflow-hidden border border-gray-600 text-[11px]">';
                html += '<button data-gateway-policy-model="' + escapeInlineHtml(m.client_model) + '" data-gateway-policy-strategy="failover" class="px-2 py-0.5 ' + (!isPinned ? 'bg-blue-600 text-white font-semibold' : 'bg-gray-800 text-gray-300 hover:bg-gray-700') + '">Failover</button>';
                html += '<button data-gateway-policy-model="' + escapeInlineHtml(m.client_model) + '" data-gateway-policy-strategy="pinned" class="px-2 py-0.5 ' + (isPinned ? 'bg-purple-600 text-white font-semibold' : 'bg-gray-800 text-gray-300 hover:bg-gray-700') + '">Pinned</button>';
                html += '</div>';
                html += '</div>';
                html += '</div>';

                html += '<div class="flex flex-wrap gap-1.5">';
                const candidates = Array.isArray(m.candidates) ? m.candidates : [];
                candidates.forEach((c, idx) => {
                    const isActive = c.provider === activeProvider;
                    const cd = c.cooldown && c.cooldown.cooling_down ? c.cooldown : null;
                    let dotClass = 'bg-gray-500';
                    let statusNote = 'unavailable';
                    if (cd) {
                        dotClass = 'bg-amber-400';
                        statusNote = 'cooldown ' + formatRelativeSeconds(cd.remaining_seconds);
                    } else if (c.available) {
                        dotClass = 'bg-green-400';
                        statusNote = 'ready';
                    }

                    const chipBorder = isActive
                        ? 'border-blue-500 bg-blue-950/50'
                        : 'border-gray-600 bg-gray-800/80';
                    const targetLabel = c.target_model && c.target_model !== m.client_model
                        ? ' → ' + c.target_model
                        : '';

                    html += '<div class="inline-flex items-center gap-1.5 border rounded px-2 py-1 text-[11px] ' + chipBorder + '" title="' + escapeInlineHtml(statusNote + ' · source: ' + (c.source || '-')) + '">';
                    html += '<span class="text-[10px] text-gray-500">#' + (idx + 1) + '</span>';
                    html += '<span class="w-2 h-2 rounded-full ' + dotClass + '"></span>';
                    html += '<span class="font-medium text-gray-100">' + escapeInlineHtml(c.provider) + '</span>';
                    if (targetLabel) {
                        html += '<code class="text-[10px] text-green-300">' + escapeInlineHtml(targetLabel) + '</code>';
                    }
                    html += '<span class="text-[10px] text-gray-500">' + escapeInlineHtml(c.source || '') + (c.legacy ? (c.available ? '·oauth' : '·no-auth') : '') + '</span>';
                    if (isActive) {
                        html += '<span class="text-[10px] text-blue-300 font-semibold">★ Active</span>';
                    } else {
                        html += '<button data-gateway-select-model="' + escapeInlineHtml(m.client_model) + '" data-gateway-select-provider="' + escapeInlineHtml(c.provider) + '" data-gateway-select-target="' + escapeInlineHtml(c.target_model || m.client_model) + '" class="ml-1 bg-gray-700 hover:bg-blue-600 text-gray-200 hover:text-white px-1.5 py-0.5 rounded text-[10px]">Use</button>';
                    }
                    html += '</div>';
                });
                html += '</div>';
                html += '</div>';
            });
            container.innerHTML = html;
        }

        const gatewayProtocolDefs = [
            { id: 'openai-chat', label: 'OpenAI Chat Completions', path: '/v1/chat/completions' },
            { id: 'openai-responses', label: 'OpenAI Responses API', path: '/v1/responses' },
            { id: 'anthropic', label: 'Anthropic Messages API', path: '/anthropic/v1/messages' },
            { id: 'gemini', label: 'Google Gemini API', path: '/v1beta/models/{model}:generateContent' }
        ];

        function renderGatewayProtocolResults() {
            const box = document.getElementById('gateway-test-results');
            if (!box) return;
            const activeEntries = gatewayProtocolDefs.filter((def) => !!gatewayProtocolResults[def.id]);
            if (activeEntries.length === 0) return;

            let html = '<table class="w-full text-left border-collapse text-xs">';
            html += '<thead><tr class="border-b border-gray-700 text-[11px] text-gray-400">';
            html += '<th class="py-1.5 px-2.5">Protocol</th>';
            html += '<th class="py-1.5 px-2.5">Status</th>';
            html += '<th class="py-1.5 px-2.5">Backend → Model</th>';
            html += '<th class="py-1.5 px-2.5">Translation / Route</th>';
            html += '<th class="py-1.5 px-2.5">Latency</th>';
            html += '<th class="py-1.5 px-2.5">Output / Error</th>';
            html += '</tr></thead><tbody>';

            activeEntries.forEach((def) => {
                const st = gatewayProtocolResults[def.id];
                let statusBadge = '<span class="px-1.5 py-0.5 rounded bg-amber-900/50 text-amber-300">Testing...</span>';
                let routeBadge = '<span class="text-gray-500">-</span>';
                let backendText = '<span class="text-gray-500">-</span>';
                let latencyText = '-';
                let outputText = '<span class="text-gray-500">Sending probe...</span>';

                if (st && st.state !== 'wait') {
                    const ok = !!st.ok;
                    statusBadge = ok
                        ? '<span class="px-1.5 py-0.5 rounded bg-green-900/60 text-green-300 font-semibold">HTTP ' + Number(st.status_code || 200) + ' OK</span>'
                        : '<span class="px-1.5 py-0.5 rounded bg-red-900/60 text-red-300 font-semibold">HTTP ' + Number(st.status_code || 0) + ' ERR</span>';
                    if (st.provider) {
                        backendText = '<span class="font-semibold text-blue-300">' + escapeInlineHtml(st.provider) + '</span> → <code class="text-green-300">' + escapeInlineHtml(st.upstream_model || st.model || '-') + '</code>';
                    }
                    if (st.route) {
                        const isPass = st.route === 'passthrough';
                        routeBadge = '<span class="px-1.5 py-0.5 rounded ' + (isPass ? 'bg-green-900/40 text-green-300' : 'bg-purple-900/50 text-purple-300') + '">' + escapeInlineHtml(st.route) + '</span>';
                        if (st.route_source) {
                            routeBadge += ' <span class="text-[10px] text-gray-500">(' + escapeInlineHtml(st.route_source) + ')</span>';
                        }
                    }
                    latencyText = Number(st.duration_ms || 0) + ' ms';
                    if (ok) {
                        const preview = st.text || st.snippet || st.summary || 'OK';
                        outputText = '<span class="text-gray-200" title="' + escapeInlineHtml(st.snippet || preview) + '">' + escapeInlineHtml(preview) + '</span>';
                    } else {
                        const errMsg = st.error || st.summary || st.snippet || 'Request failed';
                        outputText = '<span class="text-red-300" title="' + escapeInlineHtml(st.snippet || errMsg) + '">' + escapeInlineHtml(errMsg) + '</span>';
                    }
                }

                html += '<tr class="border-b border-gray-800/80">';
                html += '<td class="py-1.5 px-2.5"><div class="font-medium text-gray-200">' + escapeInlineHtml(def.label) + '</div><div class="text-[10px] font-mono text-gray-500">' + escapeInlineHtml(st.path || def.path) + '</div></td>';
                html += '<td class="py-1.5 px-2.5 whitespace-nowrap">' + statusBadge + '</td>';
                html += '<td class="py-1.5 px-2.5 font-mono text-[11px]">' + backendText + '</td>';
                html += '<td class="py-1.5 px-2.5 whitespace-nowrap text-[11px]">' + routeBadge + '</td>';
                html += '<td class="py-1.5 px-2.5 whitespace-nowrap font-mono text-gray-300">' + latencyText + '</td>';
                html += '<td class="py-1.5 px-2.5 max-w-xs truncate text-[11px]">' + outputText + '</td>';
                html += '</tr>';
            });
            html += '</tbody></table>';
            box.innerHTML = html;
        }

        async function executeGatewayProtocolProbe(protocol, model, provider, prompt) {
            gatewayProtocolResults[protocol] = { state: 'wait', model: model };
            renderGatewayProtocolResults();
            try {
                const res = await fetch('/api/gateway/test', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        protocol: protocol,
                        model: model,
                        provider: provider || '',
                        prompt: prompt || 'Reply briefly with OK.'
                    })
                });
                const data = await res.json();
                gatewayProtocolResults[protocol] = Object.assign({ state: data.ok ? 'ok' : 'err' }, data);
            } catch (e) {
                gatewayProtocolResults[protocol] = {
                    state: 'err',
                    ok: false,
                    protocol: protocol,
                    model: model,
                    status_code: 0,
                    duration_ms: 0,
                    error: e.message
                };
            }
            renderGatewayProtocolResults();
        }

        async function runSingleGatewayProtocolTest(protocol) {
            const modelEl = document.getElementById('gateway-test-model');
            const provEl = document.getElementById('gateway-test-provider');
            const promptEl = document.getElementById('gateway-test-prompt');
            const model = modelEl ? modelEl.value.trim() : '';
            const provider = provEl ? provEl.value.trim() : '';
            const prompt = promptEl ? promptEl.value.trim() : '';
            if (!model) {
                alert('Please select or enter a Model ID first.');
                return;
            }
            await executeGatewayProtocolProbe(protocol, model, provider, prompt);
        }

        async function runGatewayThreeProtocolTests() {
            const modelEl = document.getElementById('gateway-test-model');
            const provEl = document.getElementById('gateway-test-provider');
            const promptEl = document.getElementById('gateway-test-prompt');
            const model = modelEl ? modelEl.value.trim() : '';
            const provider = provEl ? provEl.value.trim() : '';
            const prompt = promptEl ? promptEl.value.trim() : '';
            if (!model) {
                alert('Please select or enter a Model ID first.');
                return;
            }
            gatewayProtocolResults = {};
            await Promise.all([
                executeGatewayProtocolProbe('openai-chat', model, provider, prompt),
                executeGatewayProtocolProbe('openai-responses', model, provider, prompt),
                executeGatewayProtocolProbe('anthropic', model, provider, prompt)
            ]);
        }

        async function probeSingleProviderModel(modelId, providerId) {
            if (!modelId || !providerId) return;
            const probeKey = providerId + '|' + modelId;
            gatewayModelProbes[probeKey] = { state: 'wait' };
            refreshProviderModelsDrawer(providerId);
            try {
                const res = await fetch('/api/gateway/test', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        protocol: 'openai-chat',
                        model: modelId,
                        provider: providerId,
                        prompt: 'Reply briefly with OK.'
                    })
                });
                const data = await res.json();
                gatewayModelProbes[probeKey] = {
                    state: data.ok ? 'ok' : 'err',
                    status: data.status_code || res.status,
                    ms: data.duration_ms || 0,
                    summary: data.ok ? (data.text || data.summary) : (data.error || data.summary)
                };
            } catch (e) {
                gatewayModelProbes[probeKey] = { state: 'err', status: 0, ms: 0, summary: e.message };
            }
            refreshProviderModelsDrawer(providerId);
        }

        async function loadGatewayDashboard() {
            const badge = document.getElementById('gateway-status-badge');
            try {
                const [provRes, modRes] = await Promise.all([
                    fetch('/api/gateway/providers', { cache: 'no-cache' }),
                    fetch('/api/gateway/models', { cache: 'no-cache' })
                ]);
                if (provRes.status === 404 || modRes.status === 404) {
                    if (badge) badge.textContent = 'Gateway admin API unavailable';
                    return;
                }
                if (provRes.ok) {
                    const provData = await provRes.json();
                    gatewayProvidersCache = provData.providers || [];
                    renderGatewayProviders(gatewayProvidersCache);
                    populateGatewayQuickProviders(gatewayProvidersCache);
                }
                if (modRes.ok) {
                    const modData = await modRes.json();
                    gatewayModelsCache = modData.models || [];
                    renderGatewayModels(gatewayModelsCache);
                }
                const testModelEl = document.getElementById('gateway-test-model');
                if (testModelEl && !testModelEl.value.trim()) {
                    const firstActive = gatewayModelsCache.find((m) => !!m.active_provider);
                    if (firstActive) {
                        testModelEl.value = firstActive.client_model;
                    } else {
                        const firstReadyProv = gatewayProvidersCache.find((p) => p.available && Array.isArray(p.models) && p.models.length > 0);
                        if (firstReadyProv) {
                            testModelEl.value = firstReadyProv.models[0];
                        }
                    }
                }
                if (badge) {
                    const readyCount = gatewayProvidersCache.filter((p) => p.available).length;
                    badge.textContent = readyCount + '/' + gatewayProvidersCache.length + ' backends ready';
                }
            } catch (e) {
                if (badge) badge.textContent = 'Failed to load gateway state';
            }
        }

        async function refreshGatewayProviderModels(providerId, btnEl) {
            if (!providerId) return;
            const oldText = btnEl ? btnEl.textContent : '';
            if (btnEl) {
                btnEl.disabled = true;
                btnEl.textContent = '...';
            }
            try {
                const res = await fetch('/api/gateway/providers/' + encodeURIComponent(providerId) + '/refresh', {
                    method: 'POST'
                });
                const data = await res.json();
                if (!res.ok) {
                    alert('Refresh ' + providerId + ' failed: ' + (data.error || res.statusText));
                }
            } catch (e) {
                alert('Refresh failed: ' + e.message);
            } finally {
                if (btnEl) {
                    btnEl.disabled = false;
                    btnEl.textContent = oldText || '↻';
                }
                await loadGatewayDashboard();
            }
        }

        function populateGatewayQuickProviders(providers) {
            const selQuick = document.getElementById('gateway-quick-provider');
            const selTest = document.getElementById('gateway-test-provider');
            const items = Array.isArray(providers) ? providers : [];

            if (selQuick) {
                const prev = selQuick.value;
                selQuick.innerHTML = '';
                items.forEach((p) => {
                    const opt = document.createElement('option');
                    opt.value = p.id;
                    opt.textContent = p.id + (p.available ? '' : ' (unconfigured)');
                    selQuick.appendChild(opt);
                });
                ['google', 'codex', 'vertex'].forEach((leg) => {
                    const opt = document.createElement('option');
                    opt.value = leg;
                    opt.textContent = leg + ' (legacy)';
                    selQuick.appendChild(opt);
                });
                if (prev) selQuick.value = prev;
            }

            if (selTest) {
                const prevTest = selTest.value;
                selTest.innerHTML = '<option value="">Auto (Follow Route / Catalog)</option>';
                items.forEach((p) => {
                    const opt = document.createElement('option');
                    opt.value = p.id;
                    opt.textContent = p.id + (p.available ? '' : ' (unconfigured)');
                    selTest.appendChild(opt);
                });
                if (prevTest) selTest.value = prevTest;
            }
        }

        async function quickSwitchGatewayBackend() {
            const clientEl = document.getElementById('gateway-quick-client');
            const provEl = document.getElementById('gateway-quick-provider');
            const targetEl = document.getElementById('gateway-quick-target');
            const stratEl = document.getElementById('gateway-quick-strategy');
            const clientModel = clientEl ? clientEl.value.trim() : '';
            const provider = provEl ? provEl.value.trim() : '';
            const targetModel = targetEl ? targetEl.value.trim() : '';
            const strategy = stratEl ? stratEl.value.trim() : '';
            if (!clientModel || !provider) {
                alert('Please specify both Client Model and Provider.');
                return;
            }
            if (strategy) {
                await setGatewayModelStrategy(clientModel, strategy);
            }
            await selectGatewayBackend(clientModel, provider, targetModel || clientModel);
        }

        async function selectGatewayBackend(clientModel, provider, targetModel) {
            if (!clientModel || !provider) return;
            try {
                const res = await fetch('/api/gateway/select', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        client_model: clientModel,
                        provider: provider,
                        target_model: targetModel || clientModel,
                        clear_cooldown: true
                    })
                });
                const data = await res.json();
                if (!res.ok) {
                    alert('Switch failed: ' + (data.error || res.statusText));
                    return;
                }
                await Promise.all([loadGatewayDashboard(), loadModelRoutes()]);
            } catch (e) {
                alert('Switch failed: ' + e.message);
            }
        }

        async function setGatewayModelStrategy(clientModel, strategy) {
            if (!clientModel || !strategy) return;
            try {
                const res = await fetch('/api/gateway/policy', {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        client_model: clientModel,
                        strategy: strategy
                    })
                });
                const data = await res.json();
                if (!res.ok) {
                    alert('Policy update failed: ' + (data.error || res.statusText));
                    return;
                }
                await loadGatewayDashboard();
            } catch (e) {
                alert('Policy update failed: ' + e.message);
            }
        }

        async function clearGatewayCooldown(providerId) {
            try {
                const payload = providerId ? { provider: providerId } : {};
                const res = await fetch('/api/gateway/cooldown/clear', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload)
                });
                if (!res.ok) {
                    const data = await res.json();
                    alert('Clear cooldown failed: ' + (data.error || res.statusText));
                    return;
                }
                await loadGatewayDashboard();
            } catch (e) {
                alert('Clear cooldown failed: ' + e.message);
            }
        }

        function initGatewayDashboardBindings() {
            const grid = document.getElementById('gateway-providers-grid');
            if (grid) {
                grid.addEventListener('click', (e) => {
                    const toggleBtn = e.target.closest('[data-gateway-toggle-models]');
                    if (toggleBtn) {
                        const pid = toggleBtn.getAttribute('data-gateway-toggle-models');
                        gatewayExpandedProviders[pid] = !gatewayExpandedProviders[pid];
                        renderGatewayProviders(gatewayProvidersCache);
                        return;
                    }
                    const refreshBtn = e.target.closest('[data-gateway-refresh-provider]');
                    if (refreshBtn) {
                        refreshGatewayProviderModels(refreshBtn.getAttribute('data-gateway-refresh-provider'), refreshBtn);
                        return;
                    }
                    const clearBtn = e.target.closest('[data-gateway-clear-provider]');
                    if (clearBtn) {
                        clearGatewayCooldown(clearBtn.getAttribute('data-gateway-clear-provider'));
                        return;
                    }
                    const pickBtn = e.target.closest('[data-gateway-pick-model]');
                    if (pickBtn) {
                        const m = pickBtn.getAttribute('data-gateway-pick-model');
                        const p = pickBtn.getAttribute('data-gateway-pick-provider');
                        fillGatewayModelSelection(m, p);
                        return;
                    }
                    const probeBtn = e.target.closest('[data-gateway-probe-model]');
                    if (probeBtn) {
                        probeSingleProviderModel(
                            probeBtn.getAttribute('data-gateway-probe-model'),
                            probeBtn.getAttribute('data-gateway-probe-provider')
                        );
                    }
                });
                grid.addEventListener('input', (e) => {
                    const searchInput = e.target.closest('[data-gateway-provider-search]');
                    if (searchInput) {
                        const pid = searchInput.getAttribute('data-gateway-provider-search');
                        gatewayProviderQueries[pid] = searchInput.value || '';
                        refreshProviderModelsDrawer(pid);
                    }
                });
            }

            const tabsEl = document.getElementById('gateway-model-tabs');
            if (tabsEl) {
                tabsEl.addEventListener('click', (e) => {
                    const tabBtn = e.target.closest('[data-gateway-tab]');
                    if (tabBtn) {
                        setGatewayModelTab(tabBtn.getAttribute('data-gateway-tab'));
                    }
                });
            }

            const modelsEl = document.getElementById('gateway-models-container');
            if (modelsEl) {
                modelsEl.addEventListener('click', (e) => {
                    const testAllBtn = e.target.closest('[data-gateway-test-all]');
                    if (testAllBtn) {
                        const m = testAllBtn.getAttribute('data-gateway-test-all');
                        const p = testAllBtn.getAttribute('data-gateway-test-prov') || '';
                        fillGatewayModelSelection(m, p);
                        runGatewayThreeProtocolTests();
                        return;
                    }
                    const useBtn = e.target.closest('[data-gateway-select-provider]');
                    if (useBtn) {
                        selectGatewayBackend(
                            useBtn.getAttribute('data-gateway-select-model'),
                            useBtn.getAttribute('data-gateway-select-provider'),
                            useBtn.getAttribute('data-gateway-select-target')
                        );
                        return;
                    }
                    const policyBtn = e.target.closest('[data-gateway-policy-strategy]');
                    if (policyBtn) {
                        setGatewayModelStrategy(
                            policyBtn.getAttribute('data-gateway-policy-model'),
                            policyBtn.getAttribute('data-gateway-policy-strategy')
                        );
                        return;
                    }
                    const fillEl = e.target.closest('[data-gateway-fill-model]');
                    if (fillEl) {
                        const m = fillEl.getAttribute('data-gateway-fill-model') || '';
                        const p = fillEl.getAttribute('data-gateway-fill-provider') || '';
                        fillGatewayModelSelection(m, p);
                    }
                });
            }
        }

        // Initial Load
        window.addEventListener('load', () => {
            initRouteProviderPolicyBindings();
            initGatewayDashboardBindings();
            refreshRouteProviderOptions('google');
            loadAll();
            loadAPIKey();
            loadSupportStatus();
            loadGatewayDashboard();
            loadModelRoutes();
            loadLoggingStatus();
            loadRequestLogs(true);
        });

        document.addEventListener('visibilitychange', () => {
            if (document.visibilityState === 'visible') {
                loadRequestLogs(true);
            }
        });
        
        // Polling
        setInterval(() => {
            loadAll();
            loadGatewayDashboard();
        }, 60000);
        setInterval(() => {
            loadRequestLogs();
        }, REQUEST_LOGS_POLL_INTERVAL_MS); // Refresh logs every 10s
    </script>
</body>
</html>`
