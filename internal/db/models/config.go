package models

import "time"

// Config stores application configuration like API keys
type Config struct {
	Key       string    `gorm:"primaryKey"` // Config key name
	Value     string    // Config value
	CreatedAt time.Time
	UpdatedAt time.Time
}
