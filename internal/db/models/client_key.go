package models

import "time"

// ClientKey is a named gateway credential issued to one caller (an app, agent, or device).
// Only the SHA-256 hash of the secret is stored; the plaintext is shown once at creation.
type ClientKey struct {
	ID      uint   `gorm:"primaryKey"`
	Name    string `gorm:"uniqueIndex;not null"`
	KeyHash string `gorm:"uniqueIndex;not null"` // hex SHA-256 of the plaintext key
	KeyHint string // masked form for display, e.g. "sk-nx-ab…9f3c"
	// AllowedProviders and AllowedModels are JSON string arrays; empty means unrestricted.
	AllowedProviders string `gorm:"type:text"`
	AllowedModels    string `gorm:"type:text"`
	Disabled         bool   `gorm:"not null;default:false"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	LastUsedAt       *time.Time
}
