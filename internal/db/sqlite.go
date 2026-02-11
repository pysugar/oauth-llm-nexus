package db

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/util"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// modelRouteCache is an in-memory cache for fast model lookups
// Key format: "clientModel:targetProvider" -> "targetModel"
var (
	modelRouteCache = make(map[string]string)
	// providerRouteCache maps clientModel -> provider for quick provider lookup
	providerRouteCache  = make(map[string]string)
	modelRouteCacheLock sync.RWMutex
)

// InitDB initializes the SQLite database connection and runs migrations.
func InitDB(dbPath string) (*gorm.DB, error) {
	gormLogLevel := logger.Warn
	if util.IsVerbose() {
		gormLogLevel = logger.Info
	}
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(gormLogLevel),
	})
	if err != nil {
		return nil, err
	}

	// Auto-migrate all models
	if err := db.AutoMigrate(&models.Account{}, &models.Config{}, &models.ModelRoute{}); err != nil {
		return nil, err
	}

	// Ensure API key exists (generate on first run)
	ensureAPIKey(db)

	// Ensure model routes are seeded from YAML if empty
	ensureModelRoutes(db)

	// Ensure default model configurations (OpenAI/Anthropic)
	ensureDefaultModels(db)

	return db, nil
}

// ensureAPIKey generates API key if not exists
func ensureAPIKey(db *gorm.DB) {
	var config models.Config
	result := db.Where("key = ?", "api_key").First(&config)

	if result.Error != nil {
		// Generate new API key: sk-<32 hex chars>
		keyBytes := make([]byte, 16)
		rand.Read(keyBytes)
		apiKey := "sk-" + hex.EncodeToString(keyBytes)

		db.Create(&models.Config{
			Key:   "api_key",
			Value: apiKey,
		})
		log.Printf("🔑 Generated new API key: %s", formatSensitiveAPIKey(apiKey))
	}
}

// GetAPIKey retrieves the API key from database
func GetAPIKey(db *gorm.DB) string {
	var config models.Config
	db.Where("key = ?", "api_key").First(&config)
	return config.Value
}

// RegenerateAPIKey creates a new API key
func RegenerateAPIKey(db *gorm.DB) string {
	keyBytes := make([]byte, 16)
	rand.Read(keyBytes)
	apiKey := "sk-" + hex.EncodeToString(keyBytes)

	db.Model(&models.Config{}).Where("key = ?", "api_key").Update("value", apiKey)
	log.Printf("🔑 Regenerated API key: %s", formatSensitiveAPIKey(apiKey))
	return apiKey
}

// ================= Model Routes =================

// YAMLRoute represents a single route entry in YAML
type YAMLRoute struct {
	Client   string `yaml:"client"`   // Client-facing model name
	Provider string `yaml:"provider"` // Backend OAuth provider (e.g., "google")
	Target   string `yaml:"target"`   // Backend model name
}

// YAMLConfig represents the structure of model_routes.yaml
type YAMLConfig struct {
	Routes []YAMLRoute `yaml:"routes"`
}

// ensureModelRoutes seeds model routes from YAML if table is empty
func ensureModelRoutes(db *gorm.DB) {
	var count int64
	db.Model(&models.ModelRoute{}).Count(&count)

	if count > 0 {
		// Already has data, just load cache
		loadModelRouteCache(db)
		return
	}

	// Try to load from config file - check multiple locations
	homeDir, _ := os.UserHomeDir()
	configPaths := []string{
		"config/model_routes.yaml",
		"./config/model_routes.yaml",
		"/etc/nexus/model_routes.yaml",
		"/opt/homebrew/etc/nexus/model_routes.yaml",
		"/usr/local/etc/nexus/model_routes.yaml",
	}
	// Add home directory paths
	if homeDir != "" {
		configPaths = append(configPaths,
			homeDir+"/.config/nexus/model_routes.yaml",
			homeDir+"/.nexus/model_routes.yaml",
		)
	}

	var data []byte
	var err error
	for _, path := range configPaths {
		data, err = os.ReadFile(path)
		if err == nil {
			log.Printf("📦 Loading model routes from: %s", path)
			break
		}
	}

	if data == nil {
		remoteURL := "https://raw.githubusercontent.com/pysugar/oauth-llm-nexus/refs/heads/main/config/model_routes.yaml"
		log.Printf("📥 Fetching default model routes from: %s", remoteURL)

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(remoteURL)
		if err == nil && resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			data, _ = io.ReadAll(resp.Body)
		} else {
			if err != nil {
				log.Printf("⚠️ Remote fetch failed: %v", err)
			} else {
				log.Printf("⚠️ Remote response error: %d", resp.StatusCode)
			}
		}
	}

	if data == nil {
		log.Printf("⚠️ No model_routes.yaml found (local or remote), using empty mappings")
		return
	}

	var config YAMLConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		log.Printf("❌ Failed to parse model_routes.yaml: %v", err)
		return
	}

	// Insert routes into database
	for _, route := range config.Routes {
		provider := route.Provider
		if provider == "" {
			provider = "google" // default
		}
		db.Create(&models.ModelRoute{
			ClientModel:    route.Client,
			TargetProvider: provider,
			TargetModel:    route.Target,
			IsActive:       true,
		})
	}

	log.Printf("✅ Seeded %d model routes from YAML", len(config.Routes))
	loadModelRouteCache(db)
}

// loadModelRouteCache loads all active routes into memory cache
// Cache key format: "clientModel:targetProvider"
func loadModelRouteCache(db *gorm.DB) {
	var routes []models.ModelRoute
	db.Where("is_active = ?", true).
		Order("client_model asc, target_provider asc, id asc").
		Find(&routes)

	modelRouteCacheLock.Lock()
	defer modelRouteCacheLock.Unlock()

	modelRouteCache = make(map[string]string)
	providerRouteCache = make(map[string]string)
	for _, r := range routes {
		key := r.ClientModel + ":" + r.TargetProvider
		modelRouteCache[key] = r.TargetModel
		// Also cache clientModel -> provider mapping (first wins)
		if _, exists := providerRouteCache[r.ClientModel]; !exists {
			providerRouteCache[r.ClientModel] = r.TargetProvider
		}
		log.Printf("  - %s -> %s (%s)", r.ClientModel, r.TargetModel, r.TargetProvider)
	}
	log.Printf("📋 Loaded %d model routes into cache", len(routes))
}

func formatSensitiveAPIKey(apiKey string) string {
	if !sensitiveLoggingEnabled() {
		return apiKey
	}
	if len(apiKey) <= 10 {
		return "***"
	}
	return apiKey[:6] + strings.Repeat("*", len(apiKey)-10) + apiKey[len(apiKey)-4:]
}

func sensitiveLoggingEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("NEXUS_MASK_SENSITIVE")))
	return v == "1" || v == "true" || v == "yes"
}

// GetModelRoute returns the target model for a given client model and target provider
func GetModelRoute(clientModel, targetProvider string) string {
	modelRouteCacheLock.RLock()
	defer modelRouteCacheLock.RUnlock()
	key := clientModel + ":" + targetProvider
	return modelRouteCache[key]
}

// ResolveModel returns the target model for a given client model and target provider
// If no mapping exists, returns the client model as-is (passthrough)
func ResolveModel(clientModel, targetProvider string) string {
	target := GetModelRoute(clientModel, targetProvider)
	if target != "" {
		log.Printf("🗺️ Model mapping: %s -> %s (provider: %s)", clientModel, target, targetProvider)
		return target
	}
	// Passthrough: no mapping found, use client model directly
	return clientModel
}

// ResolveModelWithProvider returns (targetModel, provider) for a given client model
// Lookup is based on clientModel only - provider is determined from route config
// If no mapping exists, returns (clientModel, "google") as default passthrough
func ResolveModelWithProvider(clientModel string) (targetModel, provider string) {
	modelRouteCacheLock.RLock()
	defer modelRouteCacheLock.RUnlock()

	// First find the provider for this client model
	if prov, ok := providerRouteCache[clientModel]; ok {
		key := clientModel + ":" + prov
		if target, ok := modelRouteCache[key]; ok {
			log.Printf("🗺️ Model routing: %s -> %s (provider: %s)", clientModel, target, prov)
			return target, prov
		}
	}

	// No mapping found, passthrough with default provider
	return clientModel, "google"
}

// GetAllModelRoutes returns all routes from database
func GetAllModelRoutes(db *gorm.DB) []models.ModelRoute {
	var routes []models.ModelRoute
	db.Order("target_provider, client_model").Find(&routes)
	return routes
}

// CreateModelRoute creates a new route and refreshes cache
func CreateModelRoute(db *gorm.DB, route *models.ModelRoute) error {
	if err := db.Create(route).Error; err != nil {
		return err
	}
	loadModelRouteCache(db)
	return nil
}

// UpdateModelRoute updates a route and refreshes cache
func UpdateModelRoute(db *gorm.DB, route *models.ModelRoute) error {
	if err := db.Save(route).Error; err != nil {
		return err
	}
	loadModelRouteCache(db)
	return nil
}

// DeleteModelRoute deletes a route and refreshes cache
func DeleteModelRoute(db *gorm.DB, id uint) error {
	if err := db.Delete(&models.ModelRoute{}, id).Error; err != nil {
		return err
	}
	loadModelRouteCache(db)
	return nil
}

// ResetModelRoutes clears all routes and re-seeds from YAML
func ResetModelRoutes(db *gorm.DB) error {
	if err := db.Where("1 = 1").Delete(&models.ModelRoute{}).Error; err != nil {
		return err
	}
	ensureModelRoutes(db)
	return nil
}
