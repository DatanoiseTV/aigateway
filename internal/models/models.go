package models

import (
	"time"
)

type Client struct {
	ID          string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	Name        string `gorm:"type:varchar(255);not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	APIKeyHash  []byte `gorm:"type:blob;uniqueIndex" json:"-"`
	IsActive    bool   `gorm:"default:true" json:"is_active"`
	// Backend is the provider name from config (e.g. "gemini", "openai", "anthropic", "mistral", "ollama", "lmstudio")
	Backend string `gorm:"type:varchar(50);default:'gemini'" json:"backend"`
	// BackendBaseURL allows per-client URL override for local backends (Ollama, LM Studio)
	BackendBaseURL string `gorm:"type:varchar(500)" json:"backend_base_url,omitempty"`
	// SystemPrompt is an optional system prompt prepended to every request from this client
	SystemPrompt         string    `gorm:"type:text" json:"system_prompt,omitempty"`
	RateLimitMinute      int       `gorm:"default:60" json:"rate_limit_minute"`
	RateLimitHour        int       `gorm:"default:1000" json:"rate_limit_hour"`
	RateLimitDay         int       `gorm:"default:10000" json:"rate_limit_day"`
	QuotaInputTokensDay  int       `gorm:"default:1000000" json:"quota_input_tokens_day"`
	QuotaOutputTokensDay int       `gorm:"default:500000" json:"quota_output_tokens_day"`
	QuotaRequestsDay     int       `gorm:"default:1000" json:"quota_requests_day"`
	MaxInputTokens       int       `gorm:"default:1000000" json:"max_input_tokens"`
	MaxOutputTokens      int       `gorm:"default:8192" json:"max_output_tokens"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type RequestLog struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ClientID     string    `gorm:"type:varchar(36);index" json:"client_id"`
	Model        string    `gorm:"type:varchar(100)" json:"model"`
	StatusCode   int       `json:"status_code"`
	InputTokens  int       `gorm:"default:0" json:"input_tokens"`
	OutputTokens int       `gorm:"default:0" json:"output_tokens"`
	LatencyMs    int       `json:"latency_ms"`
	ErrorMessage string    `gorm:"type:text" json:"error_message"`
	CreatedAt    time.Time `gorm:"index" json:"created_at"`
}

type DailyUsage struct {
	ID                int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ClientID          string    `gorm:"type:varchar(36);uniqueIndex:idx_client_date" json:"client_id"`
	Date              time.Time `gorm:"uniqueIndex:idx_client_date;index" json:"date"`
	TotalRequests     int       `gorm:"default:0" json:"total_requests"`
	TotalInputTokens  int       `gorm:"default:0" json:"total_input_tokens"`
	TotalOutputTokens int       `gorm:"default:0" json:"total_output_tokens"`
}

type AdminSession struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Username  string    `gorm:"type:varchar(255);uniqueIndex" json:"username"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

type Stats struct {
	TotalRequestsToday     int64   `json:"total_requests_today"`
	TotalInputTokensToday  int64   `json:"total_input_tokens_today"`
	TotalOutputTokensToday int64   `json:"total_output_tokens_today"`
	ActiveClients          int64   `json:"active_clients"`
	TotalClients           int64   `json:"total_clients"`
	ErrorRate              float64 `json:"error_rate"`
}

type ClientStats struct {
	ClientID          string `json:"client_id"`
	ClientName        string `json:"client_name"`
	RequestsToday     int    `json:"requests_today"`
	InputTokensToday  int    `json:"input_tokens_today"`
	OutputTokensToday int    `json:"output_tokens_today"`
	RequestsLimit     int    `json:"requests_limit"`
	InputTokensLimit  int    `json:"input_tokens_limit"`
	OutputTokensLimit int    `json:"output_tokens_limit"`
	MaxInputTokens    int    `json:"max_input_tokens"`
	MaxOutputTokens   int    `json:"max_output_tokens"`
}

type RateLimitInfo struct {
	Allowed     bool `json:"allowed"`
	Remaining   int  `json:"remaining"`
	ResetInSecs int  `json:"reset_in_secs"`
}

type QuotaInfo struct {
	Allowed           bool `json:"allowed"`
	RemainingRequests int  `json:"remaining_requests"`
	RemainingInput    int  `json:"remaining_input_tokens"`
	RemainingOutput   int  `json:"remaining_output_tokens"`
}
