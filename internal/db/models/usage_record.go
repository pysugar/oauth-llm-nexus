package models

// UsageRecord is one gateway call in the persisted usage ledger. Cost is not stored: it is
// computed when read, from model prices (models.dev + overrides).
type UsageRecord struct {
	ID            uint   `gorm:"primaryKey" json:"id"`
	TS            int64  `gorm:"index;index:idx_usage_ts_caller,priority:1" json:"ts"` // unix ms
	CallerID      uint   `json:"caller_id,omitempty"`
	CallerName    string `gorm:"index:idx_usage_ts_caller,priority:2" json:"caller,omitempty"`
	Provider      string `gorm:"index" json:"provider"`
	KeyFP         string `json:"key_fp,omitempty"`
	ClientModel   string `json:"client_model"`
	UpstreamModel string `json:"upstream_model,omitempty"`
	Proto         string `json:"proto,omitempty"`
	Kind          string `json:"kind,omitempty"` // chat, embeddings, rerank, gemini_action
	Route         string `json:"route,omitempty"`
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	CacheRead     int    `json:"cache_read"`
	CacheWrite    int    `json:"cache_write"`
	Reasoning     int    `json:"reasoning"`
	LatencyMS     int64  `json:"latency_ms"`
	TTFTMS        int64  `json:"ttft_ms,omitempty"`
	Status        int    `json:"status"`
	Error         string `json:"error,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
}
