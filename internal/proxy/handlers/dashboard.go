package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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

		resp, err := upstreamClient.FetchAvailableModels(cachedToken.AccessToken)
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
			result := tx.Model(&models.Account{}).Where("id = ?", id).Update("is_primary", true)
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
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"api_key": config.Value,
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
		log.Printf("🔑 Regenerated API key: %s", apiKey)
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"api_key": apiKey,
		})
	}
}

const dashboardHTML = `<!DOCTYPE html>
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
            <div class="flex justify-between items-center mb-2">
                <h3 class="text-sm font-semibold text-gray-400">🔌 API Endpoints</h3>
                <div class="flex gap-2">
                    <button onclick="testAPI()" class="text-xs bg-orange-600 hover:bg-orange-500 px-3 py-1 rounded">🧪 Test</button>
                    <button onclick="copyEndpoint()" class="text-xs bg-gray-600 hover:bg-gray-500 px-3 py-1 rounded">📋 Copy</button>
                </div>
            </div>
            <div class="grid md:grid-cols-3 gap-2 text-sm">
                <div class="bg-gray-700/50 rounded p-2"><span class="text-gray-400">OpenAI:</span> <code class="text-green-400">/v1/chat/completions</code></div>
                <div class="bg-gray-700/50 rounded p-2"><span class="text-gray-400">Claude:</span> <code class="text-blue-400">/anthropic/v1/messages</code></div>
                <div class="bg-gray-700/50 rounded p-2"><span class="text-gray-400">GenAI:</span> <code class="text-purple-400">/genai/v1beta/models</code></div>
            </div>
        </div>

        <!-- API Key Card -->
        <div class="bg-gray-800 rounded-xl p-4 mb-6">
            <div class="flex justify-between items-center mb-2">
                <h3 class="text-sm font-semibold text-gray-400">🔑 API Key</h3>
                <div class="flex gap-2">
                    <button onclick="toggleAPIKey()" id="toggle-key-btn" class="text-xs bg-gray-600 hover:bg-gray-500 px-3 py-1 rounded">👁️ Show</button>
                    <button onclick="copyAPIKey()" class="text-xs bg-gray-600 hover:bg-gray-500 px-3 py-1 rounded">📋 Copy</button>
                    <button onclick="regenerateAPIKey()" class="text-xs bg-red-600 hover:bg-red-500 px-3 py-1 rounded">🔄 Regenerate</button>
                </div>
            </div>
            <div class="bg-gray-700/50 rounded p-2 font-mono text-sm">
                <span id="api-key-display" class="text-yellow-400">Loading...</span>
                <span id="api-key-masked" class="text-gray-400" style="display:none;"></span>
            </div>
            <p class="text-xs text-gray-500 mt-2">Use this key with any SDK: <code>api_key="sk-..."</code></p>
        </div>

        <!-- Test Result Panel (shown after clicking Test button) -->
        <div id="test-panel" class="hidden bg-gray-800 rounded-xl overflow-hidden mb-6">
            <div class="px-4 py-3 border-b border-gray-700 flex justify-between items-center">
                <h2 class="font-semibold">🧪 Test Result</h2>
                <button onclick="document.getElementById('test-panel').classList.add('hidden')" class="text-gray-400 hover:text-white">✕</button>
            </div>
            <pre id="test-result" class="p-3 text-xs overflow-x-auto max-h-48"></pre>
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

        <div class="mt-6 text-center py-3 border-t border-gray-700">
            <a href="/tools" class="inline-block px-4 py-2 bg-blue-600 hover:bg-blue-500 rounded text-white text-sm font-medium mr-2">🛠️ Config Inspector</a>
            <span class="text-gray-500 text-xs"><span id="status">Ready</span> • <span class="text-gray-300 font-bold">v0.1.3</span> • <a href="/healthz" class="hover:text-gray-300">Health</a></span>
        </div>
    </div>

    <script>
        function getTierDisplay(tierStr) {
            const tier = (tierStr || 'FREE').toUpperCase();
            if (tier.includes('ULTRA')) {
                return { tier: 'Ultra', color: 'bg-gradient-to-r from-purple-500 to-pink-500' };
            } else if (tier.includes('PRO')) {
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
            const primaryBadge = acc.is_primary ? '<span class="ml-2 px-2 py-0.5 bg-yellow-500/20 text-yellow-400 rounded text-xs">⭐ Primary</span>' : '';
            
            let html = '<div class="bg-gray-800 rounded-xl overflow-hidden border border-gray-700 mb-4">';
            // Header with provider icon
            const providerIcon = acc.provider === 'google' ? '<span title="Antigravity (Google)">🚀</span>' : '<span>🔗</span>';
            html += '<div class="px-4 py-3 flex justify-between items-center border-b border-gray-700">';
            html += '<div class="flex items-center gap-2 flex-wrap">';
            html += providerIcon + '<span class="font-medium text-white">' + acc.email + '</span>' + primaryBadge;
            html += '<span class="text-xs ' + tokenColor + '">Token: ' + tokenStatus + '</span></div>';
            html += '<div class="flex items-center gap-2">';
            html += '<span class="px-2 py-1 rounded text-xs font-bold text-white ' + tier.color + '">' + tier.tier + '</span>';
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
                    const remaining = Math.round((m.quotaInfo?.remainingFraction || 0) * 100);
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

        async function testAPI() {
            document.getElementById('test-panel').classList.remove('hidden');
            document.getElementById('test-result').textContent = 'Testing...';
            try {
                const res = await fetch('/api/test');
                document.getElementById('test-result').textContent = JSON.stringify(await res.json(), null, 2);
            } catch (err) {
                document.getElementById('test-result').textContent = 'Error: ' + err.message;
            }
        }

        function copyEndpoint() {
            const url = window.location.protocol + '//' + window.location.host + '/v1';
            navigator.clipboard.writeText(url);
            document.getElementById('status').textContent = 'Copied: ' + url;
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
                        const masked = fullAPIKey.slice(0, 7) + '****' + fullAPIKey.slice(-4);
                        document.getElementById('api-key-display').textContent = masked;
                        document.getElementById('api-key-display').style.display = 'inline';
                        document.getElementById('api-key-masked').style.display = 'none';
                    } else {
                        document.getElementById('api-key-display').textContent = 'Not generated';
                    }
                }
            } catch (e) { console.error(e); }
        }

        function toggleAPIKey() {
            const display = document.getElementById('api-key-display');
            const btn = document.getElementById('toggle-key-btn');
            if (!fullAPIKey) return;

            apiKeyVisible = !apiKeyVisible;
            if (apiKeyVisible) {
                display.textContent = fullAPIKey;
                btn.textContent = '🙈 Hide';
            } else {
                const masked = fullAPIKey.slice(0, 7) + '****' + fullAPIKey.slice(-4);
                display.textContent = masked;
                btn.textContent = '👁️ Show';
            }
        }

        async function copyAPIKey() {
            if (fullAPIKey && fullAPIKey !== '') {
                navigator.clipboard.writeText(fullAPIKey);
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
        // Initial Load
        window.addEventListener('load', () => {
            loadAll();
            loadAPIKey();
            loadModelRoutes();
        });
        
        // Polling
        setInterval(loadAll, 60000);
    </script>
</body>
</html>`
