package db

import (
	"crypto/rand"
	"encoding/hex"
	"log"

	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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
	if err := db.AutoMigrate(&models.Account{}, &models.Config{}); err != nil {
		return nil, err
	}

	// Ensure API key exists (generate on first run)
	ensureAPIKey(db)

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
