package models

// RequestLog stores API request/response logs for monitoring
type RequestLog struct {
	ID           string `gorm:"primaryKey" json:"id"`
	Timestamp    int64  `gorm:"index" json:"timestamp"`
	Method       string `json:"method"`
	URL          string `json:"url"`
	Status       int    `json:"status"`
	Duration     int64  `json:"duration"` // milliseconds
	Provider     string `gorm:"index" json:"provider,omitempty"`
	Model        string `gorm:"index" json:"model,omitempty"`
	MappedModel  string `json:"mapped_model,omitempty"`
	AccountEmail string `json:"account_email,omitempty"`
	Error        string `json:"error,omitempty"`
	RequestBody  string `gorm:"type:text" json:"request_body,omitempty"`
	ResponseBody string `gorm:"type:text" json:"response_body,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
}

// RequestStats holds aggregated statistics for request logs
type RequestStats struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	ErrorCount    int64 `json:"error_count"`
}
