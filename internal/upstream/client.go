package upstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/util"
)

// Endpoints with fallback (matching Antigravity-Manager: prod → daily)
// Endpoints with fallback (daily → prod, same as oh-my-opencode)
var BaseURLs = []string{
	"https://daily-cloudcode-pa.googleapis.com/v1internal",         // daily (primary for oh-my-opencode)
	"https://cloudcode-pa.googleapis.com/v1internal",               // prod (fallback)
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal", // sandbox-daily (last resort)
}

const (
	// UserAgent mimics Antigravity's user agent (must match windows/amd64 for compatibility)
	UserAgent = "antigravity/1.11.9 windows/amd64"

	// SystemInstruction required for premium models (gemini-3-pro, Claude)
	// This is a required identity for the Cloud Code API, not a bypass
	antigravitySystemInstruction = "You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.**Absolute paths only****Proactiveness**"
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

// isPremiumModel returns true if the model requires special handling
func isPremiumModel(model string) bool {
	lowerModel := strings.ToLower(model)
	return strings.Contains(lowerModel, "claude") || strings.Contains(model, "gemini-3-pro")
}

// SmartGenerateContent automatically detects premium models and applies appropriate handling
// Premium models (gemini-3-pro, Claude) use streaming endpoint internally but consume and merge
// the stream into a single JSON response for compatibility with non-streaming handlers
// Regular models use generateContent endpoint directly
func (c *Client) SmartGenerateContent(accessToken string, payload map[string]interface{}) (*http.Response, error) {
	// Extract model name from payload
	model := ""
	if m, ok := payload["model"].(string); ok {
		model = m
	}

	// For premium models: use streaming endpoint, consume stream, merge to JSON
	if isPremiumModel(model) {
		c.enhanceForPremiumModel(payload)
		log.Printf("⚠️ [PERFORMANCE] Premium model %s in non-stream mode - consider using stream=true for better performance", model)
		log.Printf("🔄 SmartGenerateContent: using streaming endpoint for premium model %s (will merge to JSON)", model)

		// Get streaming response
		resp, err := c.doRequestWithFallback("streamGenerateContent", "alt=sse", accessToken, payload)
		if err != nil {
			return nil, err
		}

		// If not 200 OK, return the error response as-is
		if resp.StatusCode != http.StatusOK {
			return resp, nil
		}

		// Consume and merge the SSE stream into single JSON response
		mergedBody, err := c.consumeAndMergeSSE(resp)
		if err != nil {
			return nil, fmt.Errorf("failed to merge SSE stream: %w", err)
		}

		// Create a fake response with the merged body
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(mergedBody)),
			Header:     resp.Header,
		}, nil
	}

	// Regular model: add toolConfig with VALIDATED mode (CLIProxyAPI does this always)
	c.ensureToolConfig(payload)
	return c.doRequestWithFallback("generateContent", "", accessToken, payload)
}

// enhanceForPremiumModel adds required fields for premium models
func (c *Client) enhanceForPremiumModel(payload map[string]interface{}) {
	if req, ok := payload["request"].(map[string]interface{}); ok {
		// 1. Add sessionId if not present
		if _, exists := req["sessionId"]; !exists {
			req["sessionId"] = fmt.Sprintf("-%d", rand.Int63n(9_000_000_000_000_000_000))
		}

		// 2. Add toolConfig with VALIDATED mode
		if _, exists := req["toolConfig"]; !exists {
			req["toolConfig"] = map[string]interface{}{
				"functionCallingConfig": map[string]interface{}{
					"mode": "VALIDATED",
				},
			}
		}

		// 3. Add systemInstruction with Antigravity identity
		if _, exists := req["systemInstruction"]; !exists {
			req["systemInstruction"] = map[string]interface{}{
				"role": "user",
				"parts": []interface{}{
					map[string]interface{}{"text": antigravitySystemInstruction},
					map[string]interface{}{"text": fmt.Sprintf("Please ignore following [ignore]%s[/ignore]", antigravitySystemInstruction)},
				},
			}
		}
	}
}

// ensureToolConfig adds toolConfig with VALIDATED mode for regular models
func (c *Client) ensureToolConfig(payload map[string]interface{}) {
	if req, ok := payload["request"].(map[string]interface{}); ok {
		if _, exists := req["toolConfig"]; !exists {
			req["toolConfig"] = map[string]interface{}{
				"functionCallingConfig": map[string]interface{}{
					"mode": "VALIDATED",
				},
			}
		}
	}
}

// consumeAndMergeSSE reads an SSE stream and merges all chunks into a single JSON response
// This converts a streaming response to a non-streaming response for premium models
// IMPORTANT: Preserves ALL part types (text, functionCall, thoughtSignature, etc.)
func (c *Client) consumeAndMergeSSE(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var lastResponse map[string]interface{}
	var allParts []map[string]interface{}
	var textBuffer strings.Builder
	var currentIsText bool
	var traceID string
	var finishReason string
	var usageMetadata map[string]interface{}
	var role string

	// Helper to flush accumulated text into parts
	flushText := func() {
		if textBuffer.Len() > 0 {
			allParts = append(allParts, map[string]interface{}{"text": textBuffer.String()})
			textBuffer.Reset()
		}
		currentIsText = false
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Extract traceId from root level
		if tid, ok := chunk["traceId"].(string); ok && tid != "" {
			traceID = tid
		}

		// Extract the response field
		respData, ok := chunk["response"].(map[string]interface{})
		if !ok {
			// Some responses may not have "response" wrapper
			respData = chunk
		}
		lastResponse = chunk // Keep full structure for metadata

		// Extract usageMetadata
		if usage, ok := respData["usageMetadata"].(map[string]interface{}); ok {
			usageMetadata = usage
		}

		// Extract candidates
		candidates, ok := respData["candidates"].([]interface{})
		if !ok || len(candidates) == 0 {
			continue
		}

		candidate, ok := candidates[0].(map[string]interface{})
		if !ok {
			continue
		}

		// Extract finishReason
		if fr, ok := candidate["finishReason"].(string); ok && fr != "" {
			finishReason = fr
		}

		// Extract content
		content, ok := candidate["content"].(map[string]interface{})
		if !ok {
			continue
		}

		// Extract role
		if r, ok := content["role"].(string); ok && r != "" {
			role = r
		}

		// Process parts - preserve all types
		parts, ok := content["parts"].([]interface{})
		if !ok {
			continue
		}

		for _, part := range parts {
			p, ok := part.(map[string]interface{})
			if !ok {
				continue
			}

			// Check if this part has functionCall, inlineData, or other special fields
			hasFunctionCall := p["functionCall"] != nil
			hasInlineData := p["inlineData"] != nil || p["inline_data"] != nil
			hasThought := false
			if thought, ok := p["thought"].(bool); ok && thought {
				hasThought = true
			}

			// If it's a special part type, flush text buffer first and add the part
			if hasFunctionCall || hasInlineData {
				flushText()
				// Normalize inline_data to inlineData
				if inlineData, ok := p["inline_data"]; ok {
					p["inlineData"] = inlineData
					delete(p, "inline_data")
				}
				allParts = append(allParts, p)
				continue
			}

			// Handle text and thought parts - accumulate text separately
			if text, ok := p["text"].(string); ok {
				if hasThought {
					// Thought parts should be kept separate
					flushText()
					allParts = append(allParts, p)
				} else {
					// Regular text - accumulate
					if !currentIsText && textBuffer.Len() > 0 {
						flushText()
					}
					textBuffer.WriteString(text)
					currentIsText = true
				}
				continue
			}

			// Handle thoughtSignature at part level (without text)
			if sig, ok := p["thoughtSignature"].(string); ok && sig != "" {
				flushText()
				allParts = append(allParts, p)
				continue
			}

			// Any other part type - preserve as-is
			flushText()
			allParts = append(allParts, p)
		}
	}

	// Flush any remaining text
	flushText()

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanner error: %w", err)
	}

	// Build final merged response
	if lastResponse == nil {
		return []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":""}],"role":"model"}}]}}`), nil
	}

	// If no parts were collected, return empty text part
	if len(allParts) == 0 {
		allParts = []map[string]interface{}{{"text": ""}}
	}

	// Convert parts to []interface{} for JSON marshaling
	partsInterface := make([]interface{}, len(allParts))
	for i, p := range allParts {
		partsInterface[i] = p
	}

	// Update the response with merged parts
	if respData, ok := lastResponse["response"].(map[string]interface{}); ok {
		if candidates, ok := respData["candidates"].([]interface{}); ok && len(candidates) > 0 {
			if candidate, ok := candidates[0].(map[string]interface{}); ok {
				if content, ok := candidate["content"].(map[string]interface{}); ok {
					content["parts"] = partsInterface
					if role != "" {
						content["role"] = role
					}
				}
				if finishReason != "" {
					candidate["finishReason"] = finishReason
				}
			}
		}
		if usageMetadata != nil {
			respData["usageMetadata"] = usageMetadata
		}
	}

	if traceID != "" {
		lastResponse["traceId"] = traceID
	}

	return json.Marshal(lastResponse)
}

// SmartStreamGenerateContent automatically handles premium model requirements then streams
// Premium models (gemini-3-pro, Claude) need toolConfig + systemInstruction
func (c *Client) SmartStreamGenerateContent(accessToken string, payload map[string]interface{}) (*http.Response, error) {
	// Extract model name from payload
	model := ""
	if m, ok := payload["model"].(string); ok {
		model = m
	}

	// Apply premium model enhancements
	if isPremiumModel(model) {
		c.enhanceForPremiumModel(payload)
		log.Printf("🔄 SmartStreamGenerateContent: enhanced premium model %s", model)
	} else {
		c.ensureToolConfig(payload)
	}

	return c.doRequestWithFallback("streamGenerateContent", "alt=sse", accessToken, payload)
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
func EnsureRequestFormat(payload map[string]interface{}) {
	if _, ok := payload["userAgent"]; !ok {
		payload["userAgent"] = "antigravity"
	}
	if _, ok := payload["requestType"]; !ok {
		payload["requestType"] = "agent" // Restored per Antigravity-Manager reference
	}

	// Add proper toolConfig for function calling ONLY when tools are present
	// FIX: Previously this was unconditionally added, causing 429 errors for gemini-3-pro models
	if req, ok := payload["request"].(map[string]interface{}); ok {
		// Check if tools are actually present in the request
		hasTools := false
		if tools, ok := req["tools"]; ok {
			if toolsArr, ok := tools.([]interface{}); ok && len(toolsArr) > 0 {
				hasTools = true
			}
		}

		// Only add/modify toolConfig if tools are present
		if hasTools {
			if _, ok := req["toolConfig"]; !ok {
				req["toolConfig"] = map[string]interface{}{
					"functionCallingConfig": map[string]interface{}{
						"mode": "VALIDATED",
					},
				}
			} else {
				// If toolConfig exists, ensure functionCallingConfig.mode is VALIDATED
				if toolConfig, ok := req["toolConfig"].(map[string]interface{}); ok {
					if _, ok := toolConfig["functionCallingConfig"]; !ok {
						toolConfig["functionCallingConfig"] = map[string]interface{}{
							"mode": "VALIDATED",
						}
					} else if fcc, ok := toolConfig["functionCallingConfig"].(map[string]interface{}); ok {
						fcc["mode"] = "VALIDATED"
					}
				}
			}
		}
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

		// Check if we should try next endpoint (429, 403 SUBSCRIPTION_REQUIRED, or 5xx)
		// 403 is added because autopush endpoint may require subscription but prod/daily work
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500 {
			log.Printf("⚠️ Endpoint %d returned %d, trying next...", i+1, resp.StatusCode)
			lastResp = resp
			lastErr = fmt.Errorf("endpoint %d returned %d", i+1, resp.StatusCode)
			continue
		}

		// Non-retriable error (4xx except 429/403), return immediately
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
