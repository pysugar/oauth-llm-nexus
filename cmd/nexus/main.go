package main

import (
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/google"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/providers/catalog"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/handlers"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/middleware"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
)

func main() {
	// Initialize database
	database, err := db.InitDB("nexus.db")
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Initialize upstream client
	upstreamClient := upstream.NewClient()

	// Initialize proxy monitor
	proxyMonitor := monitor.NewProxyMonitor(database)

	// Initialize token manager
	tokenManager := token.NewManager(database)
	tokenManager.StartRefreshLoop()

	if google.IsUsingDefaultOAuthCredentials() {
		log.Printf("⚠️ OAuth is using built-in default client credentials. Set GOOGLE_CLIENT_ID/GOOGLE_CLIENT_SECRET for stricter credential governance.")
	}

	// Initialize Codex provider (optional, won't fail if auth.json missing)
	if err := handlers.InitCodexProvider(""); err != nil {
		log.Printf("⚠️ Codex provider not available: %v", err)
	} else {
		log.Println("✅ Codex provider initialized")
	}

	// Initialize OpenAI-compatible provider catalog (configuration + env conventions).
	if err := catalog.InitFromEnvAndConfig(); err != nil {
		log.Printf("⚠️ OpenAI-compatible provider catalog loaded with fallback/defaults: %v", err)
	} else {
		log.Println("✅ OpenAI-compatible provider catalog initialized")
	}

	// Initialize Vertex AI key proxy (auto-enabled when NEXUS_VERTEX_API_KEY is set)
	vertexAIStudioEnabled := handlers.InitVertexAIStudioProviderFromEnv()
	if vertexAIStudioEnabled {
		log.Println("✅ Vertex AI proxy enabled (/v1/publishers/google/models/*)")
	} else {
		log.Println("ℹ️ Vertex AI proxy disabled (set NEXUS_VERTEX_API_KEY to enable)")
	}

	// Initialize Gemini API proxy (auto-enabled when NEXUS_GEMINI_API_KEY or GEMINI_API_KEY is set)
	geminiAIStudioEnabled := handlers.InitGeminiAIStudioProviderFromEnv()
	if geminiAIStudioEnabled {
		log.Println("✅ Gemini API proxy enabled (/v1beta/models/*)")
	} else {
		log.Println("ℹ️ Gemini API proxy disabled (set NEXUS_GEMINI_API_KEY or GEMINI_API_KEY to enable)")
	}

	// Create router
	r := chi.NewRouter()
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)

	// ============================================
	// Public Routes (No Auth Required)
	// ============================================

	// Optional admin auth middleware
	adminPassword := os.Getenv("NEXUS_ADMIN_PASSWORD")
	optionalAdminAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if adminPassword == "" {
				next.ServeHTTP(w, r)
				return
			}
			_, pass, ok := r.BasicAuth()
			if !ok || pass != adminPassword {
				w.Header().Set("WWW-Authenticate", `Basic realm="Nexus Admin"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	// Dashboard (protected if NEXUS_ADMIN_PASSWORD is set)
	r.With(optionalAdminAuth).Get("/", handlers.DashboardHandler(database))

	// Tools page (protected if NEXUS_ADMIN_PASSWORD is set)
	r.With(optionalAdminAuth).Get("/tools", handlers.ToolsPageHandler())

	// Monitor page (protected if NEXUS_ADMIN_PASSWORD is set)
	r.With(optionalAdminAuth).Get("/monitor", handlers.MonitorPageHandler(proxyMonitor))
	r.With(optionalAdminAuth).Get("/monitor/history", handlers.MonitorHistoryPageHandler(proxyMonitor))

	// Health check endpoint (public)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// OAuth flow (uses temporary 51121 port for callback, falls back to random high port)
	r.Get("/auth/google/login", google.HandleLoginWithDB(database))
	r.Get("/auth/google/callback", google.HandleCallback(database)) // Legacy callback route

	// API routes (protected if NEXUS_ADMIN_PASSWORD is set)
	r.Route("/api", func(r chi.Router) {
		r.Use(optionalAdminAuth)
		// Account management
		r.Get("/accounts", handlers.AccountsAPIHandler(database))
		r.Get("/accounts/{id}/models", handlers.AccountModelsHandler(tokenManager, upstreamClient))
		r.Post("/accounts/{id}/promote", handlers.SetPrimaryAccountHandler(database, tokenManager))
		r.Post("/accounts/{id}/refresh", handlers.RefreshAccountHandler(tokenManager))
		r.Post("/accounts/{id}/active", handlers.UpdateAccountActiveHandler(database, tokenManager))

		// API Key management
		r.Get("/config/apikey", handlers.GetAPIKeyHandler(database))
		r.Post("/config/apikey/regenerate", handlers.RegenerateAPIKeyHandler(database))
		r.Get("/support-status", handlers.SupportStatusHandler())

		// Model Routes management
		r.Get("/model-routes", handlers.ModelRoutesHandler(database))
		r.Post("/model-routes", handlers.CreateModelRouteHandler(database))
		r.Put("/model-routes/{id}", handlers.UpdateModelRouteHandler(database))
		r.Delete("/model-routes/{id}", handlers.DeleteModelRouteHandler(database))
		r.Post("/model-routes/reset", handlers.ResetModelRoutesHandler(database))

		// Models list (aggregate from all accounts)
		r.Get("/models", handlers.ModelsHandler(tokenManager, upstreamClient))

		// Refresh tokens
		r.Post("/refresh", handlers.RefreshHandler(tokenManager))

		// Test endpoint
		r.Get("/test", handlers.TestHandler(database, tokenManager, upstreamClient, proxyMonitor))

		// Discovery
		r.Get("/discovery/scan", handlers.DiscoveryScanHandler())
		r.Get("/discovery/check", handlers.ConfigCheckHandler())
		r.Post("/discovery/import", handlers.DiscoveryImportHandler(database))

		// Version info
		r.Get("/version", handlers.VersionHandler())
		r.Get("/providers", handlers.ProvidersHandler())
		r.Get("/providers/allowed", handlers.AllowedProvidersHandler())

		// Request Monitor
		r.Get("/request-logs", handlers.GetRequestLogsHandler(proxyMonitor))
		r.Get("/request-logs/history", handlers.GetRequestLogsHistoryHandler(proxyMonitor))
		r.Get("/request-stats", handlers.GetRequestStatsHandler(proxyMonitor))
		r.Post("/request-logs/clear", handlers.ClearRequestLogsHandler(proxyMonitor))
		r.Post("/request-logs/toggle", handlers.ToggleLoggingHandler(proxyMonitor))
		r.Get("/request-logs/status", handlers.GetLoggingStatusHandler(proxyMonitor))
	})

	// ============================================
	// Protected Routes (API Key Required)
	// ============================================

	// OpenAI-compatible API
	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(database))
		r.Post("/chat/completions", handlers.OpenAIChatHandlerWithMonitor(tokenManager, upstreamClient, proxyMonitor))
		r.Get("/models", handlers.OpenAIModelsListHandler(database))
		r.Post("/responses", handlers.OpenAIResponsesHandlerWithMonitor(database, tokenManager, upstreamClient, proxyMonitor))
		r.Get("/codex/quota", handlers.CodexQuotaHandler())
		if vertexAIStudioEnabled {
			r.Post("/publishers/google/models/*", handlers.VertexAIStudioProxyHandlerWithMonitor(proxyMonitor))
		}
	})

	// OpenAI-compatible explicit provider path:
	// POST /{provider}/v1/chat/completions
	r.Route("/{provider}/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(database))
		r.Post("/chat/completions", handlers.OpenAICompatChatProxyHandlerWithMonitor(proxyMonitor))
	})

	// Anthropic-compatible API
	r.Route("/anthropic", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(database))
		r.Route("/v1", func(r chi.Router) {
			r.Post("/messages", handlers.ClaudeMessagesHandlerWithMonitor(tokenManager, upstreamClient, proxyMonitor))
			r.Get("/models", handlers.ClaudeModelsHandler(database))
		})
	})

	// GenAI-compatible API
	r.Route("/genai", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(database))
		r.Route("/v1beta/models", func(r chi.Router) {
			r.Get("/", handlers.GenAIModelsListHandlerWithMonitor(tokenManager, upstreamClient, proxyMonitor))
			r.Post("/{model}:generateContent", handlers.GenAIHandlerWithMonitor(tokenManager, upstreamClient, proxyMonitor))
			r.Post("/{model}:streamGenerateContent", handlers.GenAIStreamHandlerWithMonitor(tokenManager, upstreamClient, proxyMonitor))
		})
	})

	// Gemini API:
	// /v1beta/models
	// /v1beta/models/{model}
	// /v1beta/models/{model}:generateContent
	// /v1beta/models/{model}:streamGenerateContent
	// /v1beta/models/{model}:countTokens
	// /v1beta/models/{model}:embedContent
	// /v1beta/models/{model}:batchEmbedContents
	if geminiAIStudioEnabled {
		r.Route("/v1beta", func(r chi.Router) {
			r.Use(middleware.APIKeyAuth(database))
			r.Get("/models", handlers.GeminiAIStudioProxyHandlerWithMonitor(proxyMonitor))
			r.Get("/models/*", handlers.GeminiAIStudioProxyHandlerWithMonitor(proxyMonitor))
			r.Post("/models/*", handlers.GeminiAIStudioProxyHandlerWithMonitor(proxyMonitor))
		})
	}

	// Start server
	host := os.Getenv("HOST")
	if host == "" {
		host = "127.0.0.1" // Default to localhost, set HOST=0.0.0.0 for LAN access
	}
	port := os.Getenv("PORT")
	if port == "" {
		if os.Getenv("NEXUS_MODE") == "release" {
			port = "8086" // Default for release versions (Homebrew, etc.)
		} else {
			port = "8080" // Default for development
		}
	}

	addr := host + ":" + port
	displayURL := "localhost:" + port
	if host == "0.0.0.0" {
		displayURL = "<your-ip>:" + port
	}

	log.Printf("🚀 OAuth-LLM-Nexus starting on http://%s", addr)
	log.Printf("📊 Dashboard: http://%s", displayURL)
	log.Printf("🔌 OpenAI API: http://%s/v1", displayURL)
	log.Printf("🔌 Anthropic API: http://%s/anthropic/v1", displayURL)
	log.Printf("🔌 GenAI API: http://%s/genai/v1beta", displayURL)
	if vertexAIStudioEnabled {
		log.Printf("🔌 Vertex AI API: http://%s/v1/publishers/google/models/{model}:<action>", displayURL)
	}
	if geminiAIStudioEnabled {
		log.Printf("🔌 Gemini API: http://%s/v1beta/models", displayURL)
	}

	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
