package models

// SpendCap limits spending per period. Subject is "caller:<client key name>" or
// "key:<provider>#<upstream key fingerprint>". USD and Tokens of 0 mean no limit on that axis.
type SpendCap struct {
	ID      uint    `gorm:"primaryKey" json:"id"`
	Subject string  `gorm:"uniqueIndex:idx_cap_subject_period;not null" json:"subject"`
	Period  string  `gorm:"uniqueIndex:idx_cap_subject_period;not null" json:"period"` // day | month
	USD     float64 `json:"usd"`
	Tokens  int64   `json:"tokens"`
}
