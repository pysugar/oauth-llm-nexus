package models

import "time"

// ModelPrice is a user price override in USD per 1M tokens. Provider "*" applies to every provider.
type ModelPrice struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Provider   string    `gorm:"uniqueIndex:idx_price_provider_model;not null" json:"provider"`
	Model      string    `gorm:"uniqueIndex:idx_price_provider_model;not null" json:"model"`
	Input      float64   `json:"input"`
	Output     float64   `json:"output"`
	CacheRead  float64   `json:"cache_read"`
	CacheWrite float64   `json:"cache_write"`
	UpdatedAt  time.Time `json:"updated_at"`
}
