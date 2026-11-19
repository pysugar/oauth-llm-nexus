package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/google"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/modelmeta"
	gwprovider "github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	gwserver "github.com/pysugar/oauth-llm-nexus/internal/gateway/server"
	"github.com/pysugar/oauth-llm-nexus/internal/providers/catalog"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/handlers"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/middleware"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
	"github.com/pysugar/oauth-llm-nexus/internal/ui"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
)

func main() {
	// Initialize database
	dbPath := "nexus.db"
	database, err := db.InitDB(dbPath)
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

	// Initialize Gemini API proxy (auto-enabled when NEXUS_GEMINI_API_KEYS, NEXUS_GEMINI_API_KEY or GEMINI_API_KEY is set)
	geminiAIStudioEnabled := handlers.InitGeminiAIStudioProviderFromEnv()
	if geminiAIStudioEnabled {
		p := handlers.GeminiAIStudioProvider
		if p.KeyCount() > 1 {
			log.Printf("✅ Gemini API proxy enabled (/v1beta/models/*) [%d keys: %s]",
				p.KeyCount(), strings.Join(p.MaskedKeys(), ", "))
		} else {
			log.Printf("✅ Gemini API proxy enabled (/v1beta/models/*) [%s]",
				p.MaskedKeys()[0])
		}
	} else {
		log.Println("ℹ️ Gemini API proxy disabled (set NEXUS_GEMINI_API_KEY or GEMINI_API_KEY to enable)")
	}

	// Initialize unified multi-backend API aggregation gateway
	gatewayRegistry, regErr := gwprovider.NewRegistry()
	if regErr != nil {
		log.Printf("⚠️ Gateway provider registry loaded with defaults: %v", regErr)
	}
	if applied, err := gwserver.ApplyPersistedProviderOverrides(database, gatewayRegistry); err != nil {
		log.Printf("⚠️ Failed to load persisted gateway provider overrides: %v", err)
	} else if len(applied) > 0 {
		log.Printf("✅ Applied persisted runtime overrides for providers: %s", strings.Join(applied, ", "))
	}
	var gatewayIDs []string
	var compatSpecs []catalog.GatewayProviderSpec
	for _, p := range gatewayRegistry.List() {
		gatewayIDs = append(gatewayIDs, p.ID)
		var caps []string
		if p.Endpoints.OpenAIChat != "" {
			caps = append(caps, catalog.CapabilityOpenAIChat)
		}
		if p.Endpoints.OpenAIResponses != "" {
			caps = append(caps, catalog.CapabilityOpenAIResponses)
		}
		if p.Endpoints.Anthropic != "" {
			caps = append(caps, catalog.CapabilityAnthropicMessages)
		}
		apiKey := ""
		if p.Keys != nil {
			apiKey = p.Keys.Current()
		}
		authMode := p.AuthMode
		if p.NoKey {
			authMode = catalog.AuthModeNone
		}
		compatSpecs = append(compatSpecs, catalog.GatewayProviderSpec{
			ID:             p.ID,
			Enabled:        p.Enabled,
			RuntimeEnabled: p.Available(),
			BaseURL:        p.BaseURL(),
			AuthMode:       authMode,
			Capabilities:   caps,
			StaticHeaders:  p.StaticHeaders,
			APIKey:         apiKey,
			Timeout:        p.Timeout,
		})
	}
	db.RegisterGatewayProviders(gatewayIDs)
	catalog.RegisterGatewayProviders(compatSpecs)
	handlers.SetGatewayProviderRegistry(gatewayRegistry)

	var legacyGeminiHandler http.Handler
	if geminiAIStudioEnabled {
		legacyGeminiHandler = handlers.GeminiAIStudioProxyHandlerWithMonitor(proxyMonitor)
	}
	gatewayServer := gwserver.New(gatewayRegistry, database, proxyMonitor, gwserver.LegacyHandlers{
		OpenAIChat:        handlers.OpenAIChatHandlerWithMonitor(tokenManager, upstreamClient, proxyMonitor),
		OpenAIResponses:   handlers.OpenAIResponsesHandlerWithMonitor(database, tokenManager, upstreamClient, proxyMonitor),
		AnthropicMessages: handlers.ClaudeMessagesHandlerWithMonitor(tokenManager, upstreamClient, proxyMonitor),
		GeminiAIStudio:    legacyGeminiHandler,
		Available: func(providerID string) bool {
			switch providerID {
			case "google":
				return db.HasActiveOAuthAccounts(database)
			case "codex":
				return handlers.CodexProvider != nil
			case "vertex":
				return handlers.VertexAIStudioProvider != nil && handlers.VertexAIStudioProvider.IsEnabled()
			case "gemini":
				return handlers.GeminiAIStudioProvider != nil && handlers.GeminiAIStudioProvider.IsEnabled()
			default:
				return false
			}
		},
	})
	log.Printf("✅ Gateway server initialized (%d providers, %d available)", len(gatewayRegistry.List()), len(gatewayRegistry.Available()))
	// Pricing, context windows and modalities from models.dev (NEXUS_MODELS_DEV=off to disable).
	gatewayServer.EnableModelMeta(context.Background(), modelmeta.OptionsFromEnv(modelmeta.CachePathNear(dbPath)))
	// Persisted usage ledger (NEXUS_USAGE_RETENTION_DAYS, default 90; 0 keeps everything).
	retentionDays := 90
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("NEXUS_USAGE_RETENTION_DAYS"))); err == nil && v >= 0 {
		retentionDays = v
	}
	gatewayServer.EnableUsageLedger(time.Duration(retentionDays) * 24 * time.Hour)

	// Warm upstream model catalogs concurrently in the background so first-request routing has 0ms discovery overhead.
	go func() {
		warmCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		for _, p := range gatewayRegistry.Available() {
			wg.Add(1)
			go func(prov *gwprovider.Provider) {
				defer wg.Done()
				_, _ = gatewayRegistry.Catalog().Refresh(warmCtx, prov)
			}(p)
		}
		wg.Wait()
	}()

	// Create router
	r := chi.NewRouter()
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	// Opt-in CORS for self-hosted frontends (web/Tauri/Electron) calling inference APIs directly,
	// e.g. NEXUS_CORS_ORIGINS="http://localhost:*,tauri://localhost". Admin routes stay same-origin.
	corsOrigins := middleware.ParseCORSOrigins(os.Getenv("NEXUS_CORS_ORIGINS"))
	r.Use(middleware.CORS(corsOrigins, middleware.DefaultCORSPathPrefixes))
	if len(corsOrigins) > 0 {
		log.Printf("🌐 CORS enabled for inference APIs: %s", strings.Join(corsOrigins, ", "))
	}

	// ============================================
	// Public Routes (No Auth Required)
	// ============================================

	// Optional admin auth middleware
	adminPassword := os.Getenv("NEXUS_ADMIN_PASSWORD")
	// Browser protection for the admin surface (DNS rebinding + CSRF); see middleware.AdminGuard.
	adminGuard := middleware.AdminGuard(middleware.AdminGuardConfig{
		BindHost:   os.Getenv("HOST"),
		ExtraHosts: middleware.ParseAdminHosts(os.Getenv("NEXUS_ADMIN_HOSTS")),
	})
	optionalAdminAuth := func(next http.Handler) http.Handler {
		return adminGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if adminPassword == "" {
				next.ServeHTTP(w, r)
				return
			}
			_, pass, ok := r.BasicAuth()
			if !ok || !middleware.SecretEqual(pass, adminPassword) {
				w.Header().Set("WWW-Authenticate", `Basic realm="Nexus Admin"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}

	// Dashboard (protected if NEXUS_ADMIN_PASSWORD is set)
	r.With(optionalAdminAuth).Get("/", handlers.DashboardHandler(database))

	// Standalone Magpie-inspired Gateway UI console (protected if NEXUS_ADMIN_PASSWORD is set)
	uiHandler := ui.Handler()
	r.With(optionalAdminAuth).Get("/ui", uiHandler.ServeHTTP)
	r.With(optionalAdminAuth).Get("/ui/*", uiHandler.ServeHTTP)

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

		// API Aggregation Gateway Admin API
		r.Get("/gateway/providers", gatewayServer.HandleListProviders)
		r.Patch("/gateway/providers/{id}", gatewayServer.HandleUpdateProvider)
		r.Delete("/gateway/providers/{id}/overrides", gatewayServer.HandleClearProviderOverrides)
		r.Post("/gateway/providers/{id}/refresh", gatewayServer.HandleRefreshProvider)
		r.Get("/gateway/stats", gatewayServer.HandleGatewayStats)
		r.Post("/gateway/stats/reset", gatewayServer.HandleResetGatewayStats)
		r.Get("/gateway/models", gatewayServer.HandleListGatewayModels)
		r.Get("/gateway/models/matrix", gatewayServer.HandleModelsMatrix)
		r.Get("/gateway/models/{client}", gatewayServer.HandleGetGatewayModel)
		r.Post("/gateway/models/{client}/select", gatewayServer.HandleSelectModelBackend)
		r.Post("/gateway/models/{client}/failover", gatewayServer.HandleSyncModelFailover)
		r.Put("/gateway/models/{client}/policy", gatewayServer.HandleSetModelPolicy)
		r.Post("/gateway/select", gatewayServer.HandleSelectModelBackend)
		r.Put("/gateway/policy", gatewayServer.HandleSetModelPolicy)
		r.Post("/gateway/cooldown/clear", gatewayServer.HandleClearCooldown)
		r.Post("/gateway/test", gatewayServer.HandleGatewayTest)
		// Model prices (models.dev catalogue + user overrides)
		r.Get("/gateway/usage", gatewayServer.HandleUsage)
		r.Get("/gateway/caps", gatewayServer.HandleListCaps)
		r.Put("/gateway/caps", gatewayServer.HandlePutCap)
		r.Delete("/gateway/caps", gatewayServer.HandleDeleteCap)
		r.Get("/gateway/prices", gatewayServer.HandleListPrices)
		r.Put("/gateway/prices", gatewayServer.HandlePutPrice)
		r.Delete("/gateway/prices", gatewayServer.HandleDeletePrice)
		// Named per-caller client keys with provider/model allowlists (plaintext shown once on create)
		r.Get("/gateway/keys", gatewayServer.HandleListClientKeys)
		r.Post("/gateway/keys", gatewayServer.HandleCreateClientKey)
		r.Patch("/gateway/keys/{id}", gatewayServer.HandleUpdateClientKey)
		r.Delete("/gateway/keys/{id}", gatewayServer.HandleDeleteClientKey)

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

	// OpenAI-compatible API (plus root Anthropic /v1/messages alias)
	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(database))
		r.Post("/chat/completions", gatewayServer.HandleOpenAIChat)
		r.Get("/models", gatewayServer.HandleOpenAIModelsList)
		r.Post("/responses", gatewayServer.HandleOpenAIResponses)
		r.Post("/messages", gatewayServer.HandleAnthropicMessages)
		r.Post("/embeddings", gatewayServer.HandleOpenAIEmbeddings)
		r.Post("/rerank", gatewayServer.HandleRerank)
		// Raw (non-gateway) routes below bypass model routing; restricted client keys are gated
		// on the provider they always reach.
		r.With(middleware.RequireProvider(middleware.StaticProvider("codex"))).Get("/codex/quota", handlers.CodexQuotaHandler())
		if vertexAIStudioEnabled {
			r.With(middleware.RequireProvider(middleware.StaticProvider("vertex"))).
				Post("/publishers/google/models/*", handlers.VertexAIStudioProxyHandlerWithMonitor(proxyMonitor))
		}
	})

	// Cohere/Jina-style clients call /rerank without the /v1 prefix.
	r.With(middleware.APIKeyAuth(database)).Post("/rerank", gatewayServer.HandleRerank)

	// Generic transparent proxy for OpenAI-compatible providers: /{provider}/*
	// The sub-path is forwarded as-is to the provider's upstream (RootURL + subpath).
	// Fixed top-level routes (/v1, /anthropic, /genai, /v1beta) take priority over this
	// wildcard because chi matches more-specific (literal) segments first.
	//
	// Examples:
	//   POST /openrouter/v1/chat/completions  → https://openrouter.ai/api/v1/chat/completions
	//   POST /amd/v1/chat/completions         → https://developer.amd.com.cn/radeon/api/v1/chat/completions
	r.Route("/{provider}", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(database))
		r.Use(middleware.RequireProvider(func(r *http.Request) string { return chi.URLParam(r, "provider") }))
		r.HandleFunc("/*", handlers.GenericCompatProxyHandlerWithMonitor(proxyMonitor))
	})

	// Anthropic-compatible API
	r.Route("/anthropic", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(database))
		r.Route("/v1", func(r chi.Router) {
			r.Post("/messages", gatewayServer.HandleAnthropicMessages)
			r.Get("/models", gatewayServer.HandleAnthropicModelsList)
		})
	})

	// GenAI-compatible API (legacy Cloud Code OAuth)
	r.Route("/genai", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(database))
		r.Use(middleware.RequireProvider(middleware.StaticProvider("google")))
		r.Route("/v1beta/models", func(r chi.Router) {
			r.Get("/", handlers.GenAIModelsListHandlerWithMonitor(tokenManager, upstreamClient, proxyMonitor))
			r.Post("/{model}:generateContent", handlers.GenAIHandlerWithMonitor(tokenManager, upstreamClient, proxyMonitor))
			r.Post("/{model}:streamGenerateContent", handlers.GenAIStreamHandlerWithMonitor(tokenManager, upstreamClient, proxyMonitor))
		})
	})

	// Unified Gemini API frontend (translates to any backend or delegates non-generation actions):
	// /v1beta/models
	// /v1beta/models/{model}
	// /v1beta/models/{model}:generateContent
	// /v1beta/models/{model}:streamGenerateContent
	// /v1beta/models/{model}:countTokens
	// /v1beta/models/{model}:embedContent
	// /v1beta/models/{model}:batchEmbedContents
	// /v1beta/openai/chat/completions
	// /v1beta/openai/embeddings
	r.Route("/v1beta", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(database))
		r.Get("/models", gatewayServer.HandleGeminiModels)
		r.Get("/models/*", gatewayServer.HandleGeminiModels)
		r.Post("/models/*", gatewayServer.HandleGeminiModels)
		r.Post("/openai/chat/completions", gatewayServer.HandleOpenAIChat)
		r.Post("/openai/embeddings", gatewayServer.HandleOpenAIEmbeddings)
	})

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
	log.Printf("📊 Gateway UI: http://%s/ui", displayURL)
	log.Printf("📊 Dashboard (Legacy): http://%s", displayURL)
	log.Printf("🔌 OpenAI API: http://%s/v1 (chat/completions, responses, embeddings)", displayURL)
	log.Printf("🔌 Anthropic API: http://%s/anthropic/v1 (and /v1/messages)", displayURL)
	log.Printf("🔌 Gemini API: http://%s/v1beta/models", displayURL)
	log.Printf("🔌 GenAI API: http://%s/genai/v1beta", displayURL)
	if vertexAIStudioEnabled {
		log.Printf("🔌 Vertex AI API: http://%s/v1/publishers/google/models/{model}:<action>", displayURL)
	}

	// Graceful shutdown on SIGINT/SIGTERM (docker stop): finish in-flight requests, then flush
	// the usage ledger so no records are lost.
	httpServer := &http.Server{Addr: addr, Handler: r, ReadHeaderTimeout: 30 * time.Second}
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-sigCtx.Done()
		log.Println("🛑 Shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		gatewayServer.Ledger.Close(shutdownCtx)
	}()
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server failed: %v", err)
	}
	<-shutdownDone
}
