package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

const (
	// CodexBaseURL is the ChatGPT Backend API endpoint for Codex
	CodexBaseURL = "https://chatgpt.com/backend-api/codex"
	// UserAgent mimics Codex CLI
	UserAgent = "codex_cli_rs/0.94.0 (Mac OS 26.0.1; arm64)"
)

// Provider handles Codex API requests
type Provider struct {
	tokenMgr   *TokenManager
	httpClient *http.Client
}

// QuotaInfo contains account quota information
type QuotaInfo struct {
	Email     string `json:"email"`
	PlanType  string `json:"plan_type"`
	AccountID string `json:"account_id"`
	HasAccess bool   `json:"has_access"`
}

// NewProvider creates a new Codex Provider
func NewProvider(authPath string) *Provider {
	return &Provider{
		tokenMgr:   NewTokenManager(authPath),
		httpClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

// Init initializes the provider by loading auth.json
func (p *Provider) Init() error {
	return p.tokenMgr.Load()
}

// StreamResponses sends a request to Codex Responses API
// Returns the raw HTTP response for streaming
func (p *Provider) StreamResponses(payload map[string]interface{}) (*http.Response, error) {
	accessToken, err := p.tokenMgr.GetAccessToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get access token: %w", err)
	}

	// Force required parameters
	payload["stream"] = true
	payload["store"] = false
	if _, ok := payload["instructions"]; !ok {
		payload["instructions"] = ""
	}

	// Remove unsupported Codex parameters (per CLIProxyAPI reference)
	delete(payload, "temperature")
	delete(payload, "top_p")
	delete(payload, "max_output_tokens")
	delete(payload, "max_completion_tokens")
	delete(payload, "max_tokens")
	delete(payload, "service_tier")
	delete(payload, "presence_penalty")
	delete(payload, "frequency_penalty")

	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", CodexBaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	p.applyHeaders(req, accessToken)

	log.Printf("🔄 Codex API request: model=%v", payload["model"])
	return p.httpClient.Do(req)
}

// applyHeaders sets required headers for Codex API
func (p *Provider) applyHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Version", "0.94.0")
	req.Header.Set("Openai-Beta", "responses=experimental")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Connection", "Keep-Alive")

	// Add account ID from token
	_, _, accountID := p.tokenMgr.GetAccountInfo()
	if accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
}

// GetQuota returns the current account's quota information
func (p *Provider) GetQuota() *QuotaInfo {
	email, planType, accountID := p.tokenMgr.GetAccountInfo()
	return &QuotaInfo{
		Email:     email,
		PlanType:  planType,
		AccountID: accountID,
		HasAccess: planType == "plus" || planType == "pro" || planType == "team",
	}
}

// IsAvailable returns true if the provider is initialized and has valid tokens
func (p *Provider) IsAvailable() bool {
	_, err := p.tokenMgr.GetAccessToken()
	return err == nil
}

// ReadErrorBody reads and returns the error body from a failed response
func ReadErrorBody(resp *http.Response) string {
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
