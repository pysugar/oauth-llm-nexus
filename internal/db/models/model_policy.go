package models

import "time"

const (
	StrategyFailover = "failover"
	StrategyPinned   = "pinned"
	StrategyRotate   = "rotate"  // spread turns across healthy candidates
	StrategyUsage    = "usage"   // least recently used (decayed tokens) first
	StrategyFastest  = "fastest" // lowest latency first
)

// ValidStrategy reports whether s is a known routing strategy.
func ValidStrategy(s string) bool {
	switch s {
	case StrategyFailover, StrategyPinned, StrategyRotate, StrategyUsage, StrategyFastest:
		return true
	}
	return false
}

// ModelPolicy stores the per-model routing strategy ("failover" or "pinned").
type ModelPolicy struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ClientModel string    `gorm:"uniqueIndex;not null" json:"client_model"`
	Strategy    string    `gorm:"not null;default:'failover'" json:"strategy"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
