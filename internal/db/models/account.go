package models

import "time"

// Account stores OAuth identity and tokens for an LLM provider.
type Account struct {
	ID           string    `gorm:"primaryKey"` // UUID
	Email        string    `gorm:"uniqueIndex:idx_email_provider"`
	Provider     string    `gorm:"uniqueIndex:idx_email_provider"` // e.g., "google", "openai"
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	LastUsedAt   time.Time
	IsActive     bool   `gorm:"default:true"`
	IsPrimary    bool   `gorm:"default:false"`
	Scopes       string // JSON array of authorized scopes
	Metadata     string // JSON blob for provider-specific extras (e.g., ProjectID)
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
