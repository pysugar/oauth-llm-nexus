package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	// BaseURL is the Google internal API endpoint
	BaseURL = "https://cloudcode-pa.googleapis.com/v1internal"

	// UserAgent mimics Antigravity's user agent
	UserAgent = "antigravity/1.11.9 darwin/arm64"
)

// Client handles communication with upstream LLM APIs
type Client struct {
	httpClient *http.Client
}

// NewClient creates a new upstream client
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // Long timeout for streaming
		},
	}
}

// StreamGenerateContent calls the v1internal:streamGenerateContent endpoint
func (c *Client) StreamGenerateContent(accessToken string, payload map[string]interface{}) (*http.Response, error) {
	url := fmt.Sprintf("%s:streamGenerateContent?alt=sse", BaseURL)
	return c.doRequest("POST", url, accessToken, payload)
}

// GenerateContent calls the v1internal:generateContent endpoint (non-streaming)
func (c *Client) GenerateContent(accessToken string, payload map[string]interface{}) (*http.Response, error) {
	url := fmt.Sprintf("%s:generateContent", BaseURL)
	return c.doRequest("POST", url, accessToken, payload)
}

// FetchAvailableModels retrieves the list of available models
func (c *Client) FetchAvailableModels(accessToken string) (*http.Response, error) {
	url := fmt.Sprintf("%s:fetchAvailableModels", BaseURL)
	return c.doRequest("POST", url, accessToken, map[string]interface{}{})
}

// LoadCodeAssist fetches project configuration
func (c *Client) LoadCodeAssist(accessToken string) (string, error) {
	url := fmt.Sprintf("%s:loadCodeAssist", BaseURL)
	resp, err := c.doRequest("POST", url, accessToken, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Config struct {
			ProjectID string `json:"projectId"`
		} `json:"codeAssistConfig"`
	}

	defaultID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if defaultID == "" {
		defaultID = os.Getenv("DEFAULT_PROJECT_ID")
	}
	if defaultID == "" {
		defaultID = "bamboo-precept-lgxtn"
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return defaultID, nil
	}

	if result.Config.ProjectID != "" {
		return result.Config.ProjectID, nil
	}
	return defaultID, nil
}

// doRequest performs an HTTP request with proper headers
func (c *Client) doRequest(method, url, accessToken string, payload interface{}) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		jsonData, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
		body = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}
