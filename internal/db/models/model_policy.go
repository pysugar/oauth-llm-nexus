package models

import "time"

const (
	StrategyFailover = "failover"
	StrategyPinned   = "pinned"
)

// ModelPolicy stores the per-model routing strategy ("failover" or "pinned").
type ModelPolicy struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ClientModel string    `gorm:"uniqueIndex;not null" json:"client_model"`
	Strategy    string    `gorm:"not null;default:'failover'" json:"strategy"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
