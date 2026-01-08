package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/util"
)

// Endpoints with fallback (matching Antigravity-Manager behavior)
var BaseURLs = []string{
	"https://cloudcode-pa.googleapis.com/v1internal",               // Primary (prod)
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal", // Fallback (daily)
}

const (
	// UserAgent mimics Antigravity's user agent (must match windows/amd64 for compatibility)
	UserAgent = "antigravity/1.11.9 windows/amd64"
)

// oh-my-opencode compatible headers
var ClientMetadata = map[string]string{
	"ideType":    "IDE_UNSPECIFIED",
	"platform":   "PLATFORM_UNSPECIFIED",
	"pluginType": "GEMINI",
}

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

// StreamGenerateContent calls the v1internal:streamGenerateContent endpoint with fallback
func (c *Client) StreamGenerateContent(accessToken string, payload map[string]interface{}) (*http.Response, error) {
	return c.doRequestWithFallback("streamGenerateContent", "alt=sse", accessToken, payload)
}

// GenerateContent calls the v1internal:generateContent endpoint with fallback
func (c *Client) GenerateContent(accessToken string, payload map[string]interface{}) (*http.Response, error) {
	return c.doRequestWithFallback("generateContent", "", accessToken, payload)
}

// FetchAvailableModels retrieves the list of available models
func (c *Client) FetchAvailableModels(accessToken string) (*http.Response, error) {
	url := fmt.Sprintf("%s:fetchAvailableModels", BaseURLs[0])
	return c.doRequest("POST", url, accessToken, map[string]interface{}{})
}

// LoadCodeAssist fetches project configuration
func (c *Client) LoadCodeAssist(accessToken string) (string, error) {
	url := fmt.Sprintf("%s:loadCodeAssist", BaseURLs[0])
	// Must send metadata with ideType like Antigravity-Manager does
	resp, err := c.doRequest("POST", url, accessToken, map[string]interface{}{
		"metadata": map[string]string{"ideType": "ANTIGRAVITY"},
	})
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

// EnsureRequestFormat ensures payload has required userAgent and requestType fields
// This centralizes the format requirement that was previously scattered across handlers
func EnsureRequestFormat(payload map[string]interface{}) {
	if _, ok := payload["userAgent"]; !ok {
		payload["userAgent"] = "antigravity"
	}
	if _, ok := payload["requestType"]; !ok {
		payload["requestType"] = "agent"
	}
}

// doRequestWithFallback tries all endpoints, falling back on 429/5xx errors
func (c *Client) doRequestWithFallback(method, queryString, accessToken string, payload interface{}) (*http.Response, error) {
	var lastErr error
	var lastResp *http.Response

	// Ensure request format for map payloads
	if payloadMap, ok := payload.(map[string]interface{}); ok {
		EnsureRequestFormat(payloadMap)
	}

	for i, baseURL := range BaseURLs {
		var url string
		if queryString != "" {
			url = fmt.Sprintf("%s:%s?%s", baseURL, method, queryString)
		} else {
			url = fmt.Sprintf("%s:%s", baseURL, method)
		}

		resp, err := c.doRequest("POST", url, accessToken, payload)
		if err != nil {
			lastErr = err
			log.Printf("⚠️ Endpoint %d (%s) failed: %v", i+1, baseURL, err)
			continue
		}

		// Success or non-retriable error
		if resp.StatusCode == http.StatusOK {
			if i > 0 {
				log.Printf("✅ Fallback to endpoint %d succeeded", i+1)
			}
			return resp, nil
		}

		// Check if we should try next endpoint (429 or 5xx)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			log.Printf("⚠️ Endpoint %d returned %d, trying next...", i+1, resp.StatusCode)
			lastResp = resp
			lastErr = fmt.Errorf("endpoint %d returned %d", i+1, resp.StatusCode)
			continue
		}

		// Non-retriable error (4xx except 429), return immediately
		return resp, nil
	}

	// All endpoints failed
	if lastResp != nil {
		return lastResp, nil // Return last response so caller can read error body
	}
	return nil, lastErr
}

// doRequest performs an HTTP request with proper headers
func (c *Client) doRequest(method, url, accessToken string, payload interface{}) (*http.Response, error) {
	var body io.Reader
	var jsonData []byte
	if payload != nil {
		var err error
		jsonData, err = json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
		body = bytes.NewBuffer(jsonData)

		// Stage 2: Centralized Gemini request logging
		if util.IsVerbose() {
			prettyBytes, _ := json.MarshalIndent(payload, "", "  ")
			log.Printf("🔄 [VERBOSE] Gemini API Request Payload:\n%s", string(prettyBytes))
		}
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Standard headers
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	// oh-my-opencode compatible headers
	req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
	clientMetadataJSON, _ := json.Marshal(ClientMetadata)
	req.Header.Set("Client-Metadata", string(clientMetadataJSON))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}
