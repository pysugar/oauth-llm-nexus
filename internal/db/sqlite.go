package db

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"sync"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// modelRouteCache is an in-memory cache for fast model lookups
// Key format: "clientModel:targetProvider" -> "targetModel"
var (
	modelRouteCache     = make(map[string]string)
	modelRouteCacheLock sync.RWMutex
)

// InitDB initializes the SQLite database connection and runs migrations.
func InitDB(dbPath string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
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
		log.Printf("🔑 Generated new API key: %s", apiKey)
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
	log.Printf("🔑 Regenerated API key: %s", apiKey)
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
		log.Printf("⚠️ No model_routes.yaml found, using empty mappings (passthrough mode)")
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
	db.Where("is_active = ?", true).Find(&routes)

	modelRouteCacheLock.Lock()
	defer modelRouteCacheLock.Unlock()

	modelRouteCache = make(map[string]string)
	for _, r := range routes {
		key := r.ClientModel + ":" + r.TargetProvider
		modelRouteCache[key] = r.TargetModel
	}
	log.Printf("📋 Loaded %d model routes into cache", len(routes))
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
