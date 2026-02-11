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
		vertexEnabled := GeminiCompatProvider != nil && GeminiCompatProvider.IsEnabled()
		codexEnabled := CodexProvider != nil

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"codex_enabled":               codexEnabled,
			"gemini_vertex_proxy_enabled": vertexEnabled,
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
    <div class="container mx-auto px-4 py-6 max-w-6xl">
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
                    <span id="support-vertex" class="px-2 py-1 rounded bg-gray-700 text-gray-300">Gemini Vertex Proxy: Unknown</span>
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

        <!-- Model Routes Card -->
        <div class="bg-gray-800 rounded-xl p-4 mb-6">
            <div class="flex justify-between items-center mb-2">
                <h3 class="text-sm font-semibold text-gray-400">🗺️ Model Routes <span class="text-xs text-gray-500 ml-1">(Client → Google Backend)</span></h3>
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
                        <select id="route-provider" class="w-full bg-gray-700 border border-gray-600 rounded px-3 py-2 text-white">
                            <option value="google" selected>google</option>
                        </select>
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
        // Always mask sensitive data (emails, API keys)
        function maskEmail(email) {
            if (!email) return '-';
            const parts = email.split('@');
            if (parts.length !== 2) return 'u***r@example.com';
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
            { id: 'vertex_generate', protocol: 'Gemini Compat', path: '/v1beta/models/{model}:generateContent', model: 'gemini-3-flash-preview', supportKey: 'gemini_vertex_proxy_enabled' }
        ];

        let supportStatus = {
            codex_enabled: false,
            gemini_vertex_proxy_enabled: false
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
                        gemini_vertex_proxy_enabled: !!data.gemini_vertex_proxy_enabled
                    };
                }
            } catch (e) {
                console.error('Failed to load support status', e);
            }

            const codexBadge = document.getElementById('support-codex');
            const vertexBadge = document.getElementById('support-vertex');
            if (codexBadge) {
                codexBadge.className = supportStatus.codex_enabled
                    ? 'px-2 py-1 rounded bg-green-500/20 text-green-400'
                    : 'px-2 py-1 rounded bg-red-500/20 text-red-300';
                codexBadge.textContent = 'Codex: ' + (supportStatus.codex_enabled ? 'Enabled' : 'Disabled');
            }
            if (vertexBadge) {
                vertexBadge.className = supportStatus.gemini_vertex_proxy_enabled
                    ? 'px-2 py-1 rounded bg-green-500/20 text-green-400'
                    : 'px-2 py-1 rounded bg-red-500/20 text-red-300';
                vertexBadge.textContent = 'Gemini Vertex Proxy: ' + (supportStatus.gemini_vertex_proxy_enabled ? 'Enabled' : 'Disabled');
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
                            html += '<span class="text-purple-400 text-xs">' + r.target_provider + '</span>';
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
            document.getElementById('route-provider').value = 'google';
            document.getElementById('route-target').value = '';
            document.getElementById('route-modal').classList.remove('hidden');
        }

        function editRoute(id, client, provider, target) {
            document.getElementById('route-modal-title').textContent = 'Edit Model Route';
            document.getElementById('route-id').value = id;
            document.getElementById('route-client').value = client;
            document.getElementById('route-provider').value = provider;
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
                if (res.ok) loadModelRoutes();
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

        // Initial Load
        window.addEventListener('load', () => {
            loadAll();
            loadAPIKey();
            loadSupportStatus();
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
        setInterval(loadAll, 60000);
        setInterval(() => {
            loadRequestLogs();
        }, REQUEST_LOGS_POLL_INTERVAL_MS); // Refresh logs every 10s
    </script>
</body>
</html>`
