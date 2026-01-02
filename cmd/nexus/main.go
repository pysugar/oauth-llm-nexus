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
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/handlers"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/middleware"
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

	// Initialize token manager
	tokenManager := token.NewManager(database)
	tokenManager.StartRefreshLoop()

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

	// OAuth flow
	r.Get("/auth/google/login", google.HandleLogin)
	r.Get("/auth/google/callback", google.HandleCallback(database))

	// API routes (protected if NEXUS_ADMIN_PASSWORD is set)
	r.Route("/api", func(r chi.Router) {
		r.Use(optionalAdminAuth)
		// Account management
		r.Get("/accounts", handlers.AccountsAPIHandler(database))
		r.Get("/accounts/{id}/models", handlers.AccountModelsHandler(tokenManager, upstreamClient))
		r.Post("/accounts/{id}/promote", handlers.SetPrimaryAccountHandler(database, tokenManager))
		r.Post("/accounts/{id}/refresh", handlers.RefreshAccountHandler(tokenManager))

		// API Key management
		r.Get("/config/apikey", handlers.GetAPIKeyHandler(database))
		r.Post("/config/apikey/regenerate", handlers.RegenerateAPIKeyHandler(database))

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
		r.Get("/test", handlers.TestHandler(tokenManager, upstreamClient))

		// Discovery
		r.Get("/discovery/scan", handlers.DiscoveryScanHandler())
		r.Get("/discovery/check", handlers.ConfigCheckHandler())
		r.Post("/discovery/import", handlers.DiscoveryImportHandler(database))
	})

	// ============================================
	// Protected Routes (API Key Required)
	// ============================================

	// OpenAI-compatible API
	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(database))
		r.Post("/chat/completions", handlers.OpenAIChatHandler(tokenManager, upstreamClient))
		r.Get("/models", handlers.OpenAIModelsListHandler(tokenManager, upstreamClient))
	})

	// Anthropic-compatible API
	r.Route("/anthropic", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(database))
		r.Route("/v1", func(r chi.Router) {
			r.Post("/messages", handlers.ClaudeMessagesHandler(tokenManager, upstreamClient))
		})
	})

	// GenAI-compatible API
	r.Route("/genai", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(database))
		r.Route("/v1beta/models", func(r chi.Router) {
			r.Post("/{model}:generateContent", handlers.GenAIHandler(tokenManager, upstreamClient))
			r.Post("/{model}:streamGenerateContent", handlers.GenAIStreamHandler(tokenManager, upstreamClient))
		})
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
	log.Printf("📊 Dashboard: http://%s", displayURL)
	log.Printf("🔌 OpenAI API: http://%s/v1", displayURL)
	log.Printf("🔌 Anthropic API: http://%s/anthropic/v1", displayURL)
	log.Printf("🔌 GenAI API: http://%s/genai/v1beta", displayURL)

	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
