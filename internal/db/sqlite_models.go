package db

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
)

// ===== Model Configuration Functions =====

// ensureDefaultModels seeds default model configurations for OpenAI and Anthropic
func ensureDefaultModels(db *gorm.DB) {
	ensureOpenAIModels(db)
	ensureAnthropicModels(db)
}

// ensureOpenAIModels initializes OpenAI model configurations
func ensureOpenAIModels(db *gorm.DB) {
	var count int64
	db.Model(&models.Config{}).Where("key = ?", "openai_models").Count(&count)
	if count > 0 {
		return // Already exists
	}

	modelsList := []map[string]interface{}{
		// GPT-5.2 series (Latest, Dec 2025)
		{"id": "gpt-5.2", "object": "model", "created": 1734000000, "owned_by": "openai"},
		{"id": "gpt-5.2-instant", "object": "model", "created": 1734000000, "owned_by": "openai"},
		{"id": "gpt-5.2-thinking", "object": "model", "created": 1734000000, "owned_by": "openai"},
		{"id": "gpt-5.2-pro", "object": "model", "created": 1734000000, "owned_by": "openai"},
		{"id": "gpt-5.2-codex", "object": "model", "created": 1734000000, "owned_by": "openai"},
		{"id": "gpt-5", "object": "model", "created": 1723000000, "owned_by": "openai"},

		// GPT-4 series
		{"id": "gpt-4o", "object": "model", "created": 1715367600, "owned_by": "openai", "display_name": "GPT-4o"},
		{"id": "gpt-4o-mini", "object": "model", "created": 1721172000, "owned_by": "openai"},
		{"id": "gpt-4.1", "object": "model", "created": 1714521600, "owned_by": "openai"},
		{"id": "gpt-4-turbo", "object": "model", "created": 1712188800, "owned_by": "openai"},
		{"id": "gpt-4", "object": "model", "created": 1687882411, "owned_by": "openai", "display_name": "GPT-4"},

		// o-series (reasoning models)
		{"id": "o3", "object": "model", "created": 1737000000, "owned_by": "openai"},
		{"id": "o3-mini", "object": "model", "created": 1738000000, "owned_by": "openai"},
		{"id": "o3-pro", "object": "model", "created": 1740000000, "owned_by": "openai"},
		{"id": "o4-mini", "object": "model", "created": 1737000000, "owned_by": "openai"},
		{"id": "o4-mini-high", "object": "model", "created": 1737000000, "owned_by": "openai"},
		{"id": "o1", "object": "model", "created": 1725000000, "owned_by": "openai"},
		{"id": "o1-preview", "object": "model", "created": 1725000000, "owned_by": "openai"},
		{"id": "o1-mini", "object": "model", "created": 1725000000, "owned_by": "openai"},

		// GPT-3.5 series
		{"id": "gpt-3.5-turbo", "object": "model", "created": 1677610602, "owned_by": "openai"},
		{"id": "gpt-3.5-turbo-16k", "object": "model", "created": 1677610602, "owned_by": "openai"},
	}

	jsonData, _ := json.Marshal(modelsList)
	db.Create(&models.Config{
		Key:   "openai_models",
		Value: string(jsonData),
	})
	log.Printf("✅ Initialized default OpenAI models (%d models)", len(modelsList))
}

// ensureAnthropicModels initializes Anthropic model configurations
func ensureAnthropicModels(db *gorm.DB) {
	var count int64
	db.Model(&models.Config{}).Where("key = ?", "anthropic_models").Count(&count)
	if count > 0 {
		return
	}

	modelsList := []map[string]interface{}{
		// Claude 4.5 series (Latest, Sep-Nov 2025)
		{"id": "claude-opus-4.5", "object": "model", "created": 1732406400, "owned_by": "anthropic"},
		{"id": "claude-sonnet-4.5", "object": "model", "created": 1727568000, "owned_by": "anthropic"},
		{"id": "claude-haiku-4.5", "object": "model", "created": 1729036800, "owned_by": "anthropic"},

		// Claude 4.1
		{"id": "claude-opus-4.1", "object": "model", "created": 1722816000, "owned_by": "anthropic"},

		// Claude 4
		{"id": "claude-opus-4", "object": "model", "created": 1716595200, "owned_by": "anthropic"},
		{"id": "claude-sonnet-4", "object": "model", "created": 1716595200, "owned_by": "anthropic"},
		{"id": "claude-haiku-4", "object": "model", "created": 1716595200, "owned_by": "anthropic"},

		// Claude 3.5 series (Famous, backward compatible)
		{"id": "claude-3-5-sonnet-20241022", "object": "model", "created": 1729555200, "owned_by": "anthropic"},
		{"id": "claude-3-5-sonnet-latest", "object": "model", "created": 1729555200, "owned_by": "anthropic"},
		{"id": "claude-3-5-sonnet-20240620", "object": "model", "created": 1718841600, "owned_by": "anthropic"},
		{"id": "claude-3-5-haiku-latest", "object": "model", "created": 1729555200, "owned_by": "anthropic"},
		{"id": "claude-3-5-haiku-20241022", "object": "model", "created": 1729555200, "owned_by": "anthropic"},
		{"id": "claude-3-5-opus-latest", "object": "model", "created": 1729555200, "owned_by": "anthropic"},

		// Claude 3 series (Classic Legacy)
		{"id": "claude-3-opus", "object": "model", "created": 1709251200, "owned_by": "anthropic"},
		{"id": "claude-3-opus-20240229", "object": "model", "created": 1709251200, "owned_by": "anthropic"},
		{"id": "claude-3-sonnet", "object": "model", "created": 1709251200, "owned_by": "anthropic"},
		{"id": "claude-3-sonnet-20240229", "object": "model", "created": 1709251200, "owned_by": "anthropic"},
		{"id": "claude-3-haiku", "object": "model", "created": 1709856000, "owned_by": "anthropic"},
		{"id": "claude-3-haiku-20240307", "object": "model", "created": 1709856000, "owned_by": "anthropic"},

		// Claude 2 series (Historical)
		{"id": "claude-2.1", "object": "model", "created": 1700000000, "owned_by": "anthropic"},
		{"id": "claude-2.0", "object": "model", "created": 1690000000, "owned_by": "anthropic"},
		{"id": "claude-2", "object": "model", "created": 1690000000, "owned_by": "anthropic"},
	}

	jsonData, _ := json.Marshal(modelsList)
	db.Create(&models.Config{
		Key:   "anthropic_models",
		Value: string(jsonData),
	})
	log.Printf("✅ Initialized default Anthropic models (%d models)", len(modelsList))
}

// GetConfigModels retrieves and parses model list from config
func GetConfigModels(db *gorm.DB, key string) ([]map[string]interface{}, error) {
	var config models.Config
	if err := db.Where("key = ?", key).First(&config).Error; err != nil {
		return nil, err
	}

	var modelsList []map[string]interface{}
	if err := json.Unmarshal([]byte(config.Value), &modelsList); err != nil {
		return nil, err
	}

	// Apply display_name inference for models that don't have one
	for _, model := range modelsList {
		if _, hasDisplayName := model["display_name"]; !hasDisplayName {
			modelID, ok := model["id"].(string)
			if ok {
				model["display_name"] = InferDisplayName(modelID)
			}
		}
	}

	return modelsList, nil
}

// GetClientModelsSet returns a Set (map) of all routed client models
func GetClientModelsSet(db *gorm.DB) map[string]bool {
	var routes []models.ModelRoute
	db.Where("is_active = ?", true).Find(&routes)

	set := make(map[string]bool)
	for _, r := range routes {
		set[r.ClientModel] = true
	}
	return set
}

// InferDisplayName generates a human-readable display name from model ID
func InferDisplayName(modelID string) string {
	// Special cases that don't follow the general pattern
	specialCases := map[string]string{
		"gpt-4o":          "GPT-4o",
		"gpt-4o-mini":     "GPT-4o Mini",
		"gpt-3.5-turbo":   "GPT-3.5 Turbo",
		"gpt-3.5-turbo-16k": "GPT-3.5 Turbo 16K",
		"o1-preview":      "o1 Preview",
		"o1-mini":         "o1 Mini",
		"o3-mini":         "o3 Mini",
		"o3-pro":          "o3 Pro",
		"o4-mini":         "o4 Mini",
		"o4-mini-high":    "o4 Mini High",
	}

	if name, ok := specialCases[modelID]; ok {
		return name
	}

	// General pattern: replace hyphens with spaces and capitalize
	name := strings.ReplaceAll(modelID, "-", " ")
	words := strings.Fields(name)

	for i, word := range words {
		// Check if word contains only digits (version numbers)
		isNumeric := true
		for _, r := range word {
			if !((r >= '0' && r <= '9') || r == '.') {
				isNumeric = false
				break
			}
		}

		if !isNumeric {
			// Capitalize first letter
			if len(word) > 0 {
				words[i] = strings.ToUpper(word[:1]) + word[1:]
			}
		}
	}

	return strings.Join(words, " ")
}
