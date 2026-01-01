package models

import (
	"time"
)

// ModelRoute defines a mapping from client model ID to target model ID for a specific backend provider
// The combination of (ClientModel, TargetProvider) must be unique
// - ClientModel: The model name from client request (e.g., "gpt-4", "claude-3-sonnet")
// - TargetProvider: The backend OAuth provider (e.g., "google", potentially "openai" in future)
// - TargetModel: The actual model name on the backend (e.g., "gemini-3-pro-high")
type ModelRoute struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ClientModel    string    `gorm:"uniqueIndex:idx_model_provider;not null" json:"client_model"`    // e.g., "gpt-4"
	TargetProvider string    `gorm:"uniqueIndex:idx_model_provider;not null;default:'google'" json:"target_provider"` // e.g., "google"
	TargetModel    string    `gorm:"not null" json:"target_model"`                                   // e.g., "gemini-3-pro-high"
	IsActive       bool      `gorm:"default:true" json:"is_active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
