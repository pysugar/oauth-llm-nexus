package db

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
	// providerRouteCache maps clientModel -> primary provider (first priority/id wins)
	providerRouteCache  = make(map[string]string)
	orderedRoutesCache  = make(map[string][]models.ModelRoute)
	modelPolicyCache    = make(map[string]string)
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
	if err := db.AutoMigrate(&models.Account{}, &models.Config{}, &models.ModelRoute{}, &models.ModelPolicy{}, &models.ClientKey{}); err != nil {
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

// apiKeyCache memoizes the gateway API key per database handle so request auth
// does not hit SQLite on every call. RegenerateAPIKey refreshes it.
var apiKeyCache sync.Map // map[*gorm.DB]string

// InvalidateAPIKeyCache drops the memoized API key for db after an out-of-band update.
func InvalidateAPIKeyCache(db *gorm.DB) {
	apiKeyCache.Delete(db)
}

// GetAPIKey retrieves the API key from database
func GetAPIKey(db *gorm.DB) string {
	if v, ok := apiKeyCache.Load(db); ok {
		return v.(string)
	}
	var config models.Config
	db.Where("key = ?", "api_key").First(&config)
	if config.Value != "" {
		apiKeyCache.Store(db, config.Value)
	}
	return config.Value
}

// RegenerateAPIKey creates a new API key
func RegenerateAPIKey(db *gorm.DB) string {
	keyBytes := make([]byte, 16)
	rand.Read(keyBytes)
	apiKey := "sk-" + hex.EncodeToString(keyBytes)

	db.Model(&models.Config{}).Where("key = ?", "api_key").Update("value", apiKey)
	apiKeyCache.Store(db, apiKey)
	log.Printf("🔑 Regenerated API key: %s", formatSensitiveAPIKey(apiKey))
	return apiKey
}

// ================= Model Routes =================

// YAMLRoute represents a single route entry in YAML
type YAMLRoute struct {
	Client   string `yaml:"client"`   // Client-facing model name
	Provider string `yaml:"provider"` // Backend provider (e.g., "google", "openai", "deepseek")
	Target   string `yaml:"target"`   // Backend model name
	Priority int    `yaml:"priority"` // Optional ordering priority (lower wins)
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
		provider := NormalizeProvider(route.Provider)
		db.Create(&models.ModelRoute{
			ClientModel:    route.Client,
			TargetProvider: provider,
			TargetModel:    route.Target,
			Priority:       route.Priority,
			IsActive:       true,
		})
	}

	log.Printf("✅ Seeded %d model routes from YAML", len(config.Routes))
	loadModelRouteCache(db)
}

// loadModelRouteCache loads all active routes into memory cache
// Cache key format: "clientModel:targetProvider"
// RefreshModelRouteCache reloads active routes and model policies into memory.
func RefreshModelRouteCache(db *gorm.DB) {
	loadModelRouteCache(db)
}

func loadModelRouteCache(db *gorm.DB) {
	var routes []models.ModelRoute
	db.Where("is_active = ?", true).
		Order("priority asc, id asc").
		Find(&routes)

	policies := make(map[string]string)
	if db.Migrator().HasTable(&models.ModelPolicy{}) {
		var policyRows []models.ModelPolicy
		db.Find(&policyRows)
		for _, p := range policyRows {
			policies[p.ClientModel] = normalizePolicyStrategy(p.Strategy)
		}
	}

	modelRouteCacheLock.Lock()
	defer modelRouteCacheLock.Unlock()

	modelRouteCache = make(map[string]string)
	providerRouteCache = make(map[string]string)
	orderedRoutesCache = make(map[string][]models.ModelRoute)
	modelPolicyCache = policies

	for _, r := range routes {
		provider := NormalizeProvider(r.TargetProvider)
		r.TargetProvider = provider
		key := r.ClientModel + ":" + provider
		modelRouteCache[key] = r.TargetModel
		orderedRoutesCache[r.ClientModel] = append(orderedRoutesCache[r.ClientModel], r)
		// Also cache clientModel -> primary provider mapping (highest priority / lowest id wins)
		if _, exists := providerRouteCache[r.ClientModel]; !exists {
			providerRouteCache[r.ClientModel] = provider
		}
		log.Printf("  - %s -> %s (%s, priority=%d)", r.ClientModel, r.TargetModel, provider, r.Priority)
	}
	log.Printf("📋 Loaded %d model routes into cache", len(routes))
}

func normalizePolicyStrategy(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), models.StrategyPinned) {
		return models.StrategyPinned
	}
	return models.StrategyFailover
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
	key := clientModel + ":" + NormalizeProvider(targetProvider)
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

// ResolveModelWithProvider returns (targetModel, provider) for a given client model.
// Deprecated: prefer ResolveModelWithProviderForProtocol to enforce protocol/provider compatibility.
// This helper is retained for compatibility with tests and probe endpoints.
// If route validation fails, it falls back to (clientModel, "google").
func ResolveModelWithProvider(clientModel string) (targetModel, provider string) {
	targetModel, provider, err := ResolveModelWithProviderForProtocol(clientModel, "")
	if err != nil {
		log.Printf("⚠️ Model routing invalid for %s: %v (fallback to google passthrough)", clientModel, err)
		return clientModel, "google"
	}
	return targetModel, provider
}

// ResolveModelWithProviderForProtocol resolves route using first active row (id ASC),
// then validates model-prefix policy and protocol/provider compatibility.
func ResolveModelWithProviderForProtocol(clientModel string, protocol string) (targetModel, provider string, err error) {
	clientModel = strings.TrimSpace(clientModel)
	if clientModel == "" {
		return "", "", fmt.Errorf("client model is required")
	}

	modelRouteCacheLock.RLock()
	defer modelRouteCacheLock.RUnlock()

	provider = "google"
	targetModel = clientModel

	if prov, ok := providerRouteCache[clientModel]; ok {
		provider = NormalizeProvider(prov)
		key := clientModel + ":" + provider
		if target, exists := modelRouteCache[key]; exists {
			targetModel = target
		}
	}

	if err := ValidateRouteProvider(clientModel, provider); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(protocol) != "" {
		if err := ValidateProviderForProtocol(provider, protocol); err != nil {
			return "", "", err
		}
	}

	log.Printf("🗺️ Model routing: %s -> %s (provider: %s, protocol: %s)", clientModel, targetModel, provider, normalizeProtocolForLog(protocol))
	return targetModel, provider, nil
}

func normalizeProtocolForLog(protocol string) string {
	p := strings.TrimSpace(strings.ToLower(protocol))
	if p == "" {
		return "any"
	}
	return p
}

// GetAllModelRoutes returns all routes from database ordered by client_model and priority.
func GetAllModelRoutes(db *gorm.DB) []models.ModelRoute {
	var routes []models.ModelRoute
	db.Order("client_model, priority asc, id asc").Find(&routes)
	return routes
}

// GetModelCandidates returns ordered active candidate routes for clientModel.
func GetModelCandidates(clientModel string) []models.ModelRoute {
	modelRouteCacheLock.RLock()
	defer modelRouteCacheLock.RUnlock()
	src := orderedRoutesCache[clientModel]
	if len(src) == 0 {
		return nil
	}
	out := make([]models.ModelRoute, len(src))
	copy(out, src)
	return out
}

// GetAllModelCandidates returns a snapshot of all ordered active routes keyed by clientModel.
func GetAllModelCandidates() map[string][]models.ModelRoute {
	modelRouteCacheLock.RLock()
	defer modelRouteCacheLock.RUnlock()
	out := make(map[string][]models.ModelRoute, len(orderedRoutesCache))
	for k, v := range orderedRoutesCache {
		cp := make([]models.ModelRoute, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// SelectModelProvider promotes targetProvider to Priority 0 for clientModel and shifts other
// candidates down in deterministic order, creating the route if it does not yet exist.
func SelectModelProvider(db *gorm.DB, clientModel, targetProvider, targetModel string) error {
	clientModel = strings.TrimSpace(clientModel)
	targetProvider = NormalizeProvider(targetProvider)
	if clientModel == "" || targetProvider == "" {
		return fmt.Errorf("clientModel and targetProvider are required")
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		var existing []models.ModelRoute
		if err := tx.Where("client_model = ?", clientModel).Order("priority asc, id asc").Find(&existing).Error; err != nil {
			return err
		}

		var selected *models.ModelRoute
		var others []models.ModelRoute
		for i := range existing {
			if NormalizeProvider(existing[i].TargetProvider) == targetProvider {
				copyRow := existing[i]
				selected = &copyRow
			} else {
				others = append(others, existing[i])
			}
		}

		if selected == nil {
			mapped := strings.TrimSpace(targetModel)
			if mapped == "" {
				mapped = clientModel
			}
			row := models.ModelRoute{
				ClientModel:    clientModel,
				TargetProvider: targetProvider,
				TargetModel:    mapped,
				Priority:       0,
				IsActive:       true,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		} else {
			updates := map[string]any{
				"priority":  0,
				"is_active": true,
			}
			if m := strings.TrimSpace(targetModel); m != "" {
				updates["target_model"] = m
			}
			if err := tx.Model(&models.ModelRoute{}).Where("id = ?", selected.ID).Updates(updates).Error; err != nil {
				return err
			}
		}

		for idx, other := range others {
			if err := tx.Model(&models.ModelRoute{}).Where("id = ?", other.ID).Update("priority", idx+1).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	loadModelRouteCache(db)
	return nil
}

// GetModelPolicy returns the routing strategy ("failover" or "pinned") for clientModel.
func GetModelPolicy(clientModel string) string {
	modelRouteCacheLock.RLock()
	defer modelRouteCacheLock.RUnlock()
	if s, ok := modelPolicyCache[clientModel]; ok && s != "" {
		return s
	}
	return models.StrategyFailover
}

// GetAllModelPolicies returns all configured per-model strategies.
func GetAllModelPolicies() map[string]string {
	modelRouteCacheLock.RLock()
	defer modelRouteCacheLock.RUnlock()
	out := make(map[string]string, len(modelPolicyCache))
	for k, v := range modelPolicyCache {
		out[k] = v
	}
	return out
}

// SetModelPolicy persists the routing strategy for clientModel and updates the cache.
func SetModelPolicy(db *gorm.DB, clientModel, strategy string) error {
	clientModel = strings.TrimSpace(clientModel)
	if clientModel == "" {
		return fmt.Errorf("client_model is required")
	}
	norm := strings.ToLower(strings.TrimSpace(strategy))
	if norm != models.StrategyFailover && norm != models.StrategyPinned {
		return fmt.Errorf("invalid strategy %q (must be %q or %q)", strategy, models.StrategyFailover, models.StrategyPinned)
	}

	var existing models.ModelPolicy
	res := db.Where("client_model = ?", clientModel).First(&existing)
	if res.Error == nil {
		if err := db.Model(&existing).Update("strategy", norm).Error; err != nil {
			return err
		}
	} else {
		if err := db.Create(&models.ModelPolicy{
			ClientModel: clientModel,
			Strategy:    norm,
		}).Error; err != nil {
			return err
		}
	}
	loadModelRouteCache(db)
	return nil
}

// HasActiveOAuthAccounts returns true when at least one active legacy OAuth account exists.
func HasActiveOAuthAccounts(db *gorm.DB) bool {
	if db == nil || !db.Migrator().HasTable(&models.Account{}) {
		return false
	}
	var count int64
	db.Model(&models.Account{}).Where("is_active = ?", true).Count(&count)
	return count > 0
}

// CreateModelRoute creates a new route and refreshes cache
func CreateModelRoute(db *gorm.DB, route *models.ModelRoute) error {
	route.TargetProvider = NormalizeProvider(route.TargetProvider)
	if err := db.Create(route).Error; err != nil {
		return err
	}
	loadModelRouteCache(db)
	return nil
}

// UpdateModelRoute updates a route and refreshes cache
func UpdateModelRoute(db *gorm.DB, route *models.ModelRoute) error {
	route.TargetProvider = NormalizeProvider(route.TargetProvider)
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
