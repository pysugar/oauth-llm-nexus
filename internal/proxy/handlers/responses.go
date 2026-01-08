package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/mappers"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
	"gorm.io/gorm"
)

// ===== Responses API Data Structures (OpenAI Spec Compliant) =====

// OpenAIResponsesRequest represents /v1/responses request
type OpenAIResponsesRequest struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input"` // Can be string or []ResponsesInputMessage
	Instructions       string          `json:"instructions,omitempty"`
	Tools              []ResponsesTool `json:"tools,omitempty"`
	MaxOutputTokens    *int            `json:"max_output_tokens,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	Stream             bool            `json:"stream,omitempty"`
	Conversation       string          `json:"conversation,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Modalities         []string        `json:"modalities,omitempty"`
}

// ResponsesTool represents a tool configuration (OpenAI Responses API)
// Supports: web_search, file_search, function, code_interpreter, mcp
type ResponsesTool struct {
	Type string `json:"type"` // "web_search", "file_search", "function", "code_interpreter", "mcp"

	// web_search specific fields
	UserLocation      *ToolUserLocation `json:"user_location,omitempty"`
	Filters           *ToolFilters      `json:"filters,omitempty"`
	ExternalWebAccess *bool             `json:"external_web_access,omitempty"`

	// function specific fields (when type="function")
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Strict      *bool                  `json:"strict,omitempty"`

	// file_search specific fields
	VectorStoreIDs []string `json:"vector_store_ids,omitempty"`
	Files          []string `json:"files,omitempty"`
}

// ToolFilters for web_search domain filtering
type ToolFilters struct {
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

// ToolUserLocation for web_search tool localization
type ToolUserLocation struct {
	Type    string `json:"type,omitempty"` // "approximate"
	Country string `json:"country,omitempty"`
	City    string `json:"city,omitempty"`
	Region  string `json:"region,omitempty"`
}

// ResponsesInputMessage represents a message in the input array
type ResponsesInputMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // Can be string or []ResponsesContent
}

// ResponsesContent represents content item (input or output)
type ResponsesContent struct {
	Type        string                     `json:"type"` // input_text, input_image, input_file, output_text, text
	Text        string                     `json:"text,omitempty"`
	ImageURL    string                     `json:"image_url,omitempty"`
	FileID      string                     `json:"file_id,omitempty"`
	Annotations []mappers.OpenAIAnnotation `json:"annotations,omitempty"` // URL citations from grounding
}

// OpenAIResponsesResponse represents /v1/responses response (OpenAI spec)
type OpenAIResponsesResponse struct {
	ID        string                `json:"id"`         // "resp_xxx" format
	Object    string                `json:"object"`     // "response"
	Status    string                `json:"status"`     // "completed", "in_progress", "failed"
	CreatedAt int64                 `json:"created_at"` // Unix timestamp
	Model     string                `json:"model"`
	Output    []ResponsesOutputItem `json:"output"`
	Usage     *ResponsesUsage       `json:"usage,omitempty"`
	Error     *ResponsesError       `json:"error,omitempty"`
}

// ResponsesOutputItem represents an item in the output array
type ResponsesOutputItem struct {
	ID      string             `json:"id"`               // "item_xxx" format
	Type    string             `json:"type"`             // "message", "tool_call", "tool_output"
	Role    string             `json:"role,omitempty"`   // "assistant"
	Status  string             `json:"status,omitempty"` // "completed"
	Content []ResponsesContent `json:"content,omitempty"`
}

// ResponsesMessage represents a message in the response (legacy support)
type ResponsesMessage struct {
	Role    string             `json:"role"`
	Content []ResponsesContent `json:"content"`
}

// ResponsesUsage represents token usage
type ResponsesUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ResponsesError represents an error in the response
type ResponsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ===== Conversion Functions =====

// ConvertResponsesToChatCompletion converts Responses API format to Chat Completions
// Note: tools (web_search, file_search, etc.) are passed through but may not be fully
// supported by the Gemini backend. A warning is logged when tools are present.
func ConvertResponsesToChatCompletion(req OpenAIResponsesRequest) mappers.OpenAIChatRequest {
	chatReq := mappers.OpenAIChatRequest{
		Model:       req.Model,
		Stream:      req.Stream,
		Temperature: req.Temperature,
	}

	// Convert max_output_tokens to max_tokens
	if req.MaxOutputTokens != nil {
		chatReq.MaxTokens = req.MaxOutputTokens
	}

	// Convert tools to mappers.Tool format for Gemini
	if len(req.Tools) > 0 {
		var mappedTools []mappers.Tool
		for _, tool := range req.Tools {
			mappedTool := mappers.Tool{
				Type: tool.Type, // web_search, web_search_preview, function, etc.
			}

			// Handle function type (Responses API has name/parameters on tool object directly)
			if tool.Type == "function" && tool.Name != "" {
				mappedTool.Function = &mappers.FunctionDefinition{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.Parameters,
				}
			}

			// Map user_location if present (for web_search)
			if tool.UserLocation != nil {
				mappedTool.UserLocation = &mappers.UserLocation{
					Type: tool.UserLocation.Type,
					Approximate: &mappers.ApproximateLocation{
						Country: tool.UserLocation.Country,
						City:    tool.UserLocation.City,
						Region:  tool.UserLocation.Region,
					},
				}
			}
			mappedTools = append(mappedTools, mappedTool)
		}
		chatReq.Tools = mappedTools

		if IsVerbose() {
			var toolTypes []string
			for _, tool := range req.Tools {
				toolTypes = append(toolTypes, tool.Type)
			}
			log.Printf("🔧 /v1/responses: converting tools (%v) to Gemini format", toolTypes)
		}
	}

	// Initialize messages slice
	var messages []mappers.OpenAIMessage

	// Handle instructions as system message
	if req.Instructions != "" {
		messages = append(messages, mappers.OpenAIMessage{
			Role:    "system",
			Content: req.Instructions,
		})
	}

	// Parse input - can be string or []ResponsesInputMessage
	inputMessages := parseResponsesInput(req.Input)
	for _, inputMsg := range inputMessages {
		messages = append(messages, inputMsg)
	}

	chatReq.Messages = messages
	return chatReq
}

// parseResponsesInput parses the flexible input field
func parseResponsesInput(input json.RawMessage) []mappers.OpenAIMessage {
	if len(input) == 0 {
		return nil
	}

	// Try parsing as a simple string first
	var simpleInput string
	if err := json.Unmarshal(input, &simpleInput); err == nil {
		return []mappers.OpenAIMessage{
			{Role: "user", Content: simpleInput},
		}
	}

	// Try parsing as array of messages
	var inputMessages []ResponsesInputMessage
	if err := json.Unmarshal(input, &inputMessages); err == nil {
		result := make([]mappers.OpenAIMessage, 0, len(inputMessages))
		for _, inputMsg := range inputMessages {
			contentText := parseResponsesContent(inputMsg.Content)
			result = append(result, mappers.OpenAIMessage{
				Role:    inputMsg.Role,
				Content: contentText,
			})
		}
		return result
	}

	return nil
}

// parseResponsesContent parses content field (string or []ResponsesContent)
func parseResponsesContent(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}

	// Try parsing as a simple string first
	var simpleContent string
	if err := json.Unmarshal(content, &simpleContent); err == nil {
		return simpleContent
	}

	// Try parsing as array of content items
	var contentItems []ResponsesContent
	if err := json.Unmarshal(content, &contentItems); err == nil {
		var textParts []string
		for _, item := range contentItems {
			if item.Type == "input_text" || item.Type == "text" {
				textParts = append(textParts, item.Text)
			}
			// TODO: Handle input_image, input_file in future
		}
		return strings.Join(textParts, "\n")
	}

	return ""
}

// ConvertChatCompletionToResponses converts Chat Completions response to Responses format
func ConvertChatCompletionToResponses(chatResp map[string]interface{}) OpenAIResponsesResponse {
	// Generate proper response ID (resp_xxx format)
	respID := "resp_" + uuid.New().String()[:12]

	// Get created timestamp
	var createdAt int64
	if created, ok := chatResp["created"].(float64); ok {
		createdAt = int64(created)
	}

	resp := OpenAIResponsesResponse{
		ID:        respID,
		Object:    "response",
		Status:    "completed",
		CreatedAt: createdAt,
	}
	// Safe type assertion with fallback (panic guard)
	if model, ok := chatResp["model"].(string); ok {
		resp.Model = model
	} else {
		resp.Model = "unknown"
	}

	// Convert choices to output items
	chatChoices, ok := chatResp["choices"].([]interface{})
	if !ok || len(chatChoices) == 0 {
		return resp
	}

	resp.Output = make([]ResponsesOutputItem, len(chatChoices))

	for i, choice := range chatChoices {
		chatChoice, ok := choice.(map[string]interface{})
		if !ok {
			continue
		}
		message, ok := chatChoice["message"].(map[string]interface{})
		if !ok {
			continue
		}

		// Handle content safely
		contentText := ""
		if content, ok := message["content"].(string); ok {
			contentText = content
		}

		// Generate item ID
		itemID := "item_" + uuid.New().String()[:8]

		// Get role
		role := "assistant"
		if r, ok := message["role"].(string); ok {
			role = r
		}

		// Create message output item (new structure without nested Message)
		resp.Output[i] = ResponsesOutputItem{
			ID:     itemID,
			Type:   "message",
			Role:   role,
			Status: "completed",
			Content: []ResponsesContent{
				{
					Type: "output_text",
					Text: contentText,
				},
			},
		}
	}

	// Convert usage (with nil check)
	if usageData, ok := chatResp["usage"].(map[string]interface{}); ok && usageData != nil {
		usage := &ResponsesUsage{}
		if pt, ok := usageData["prompt_tokens"].(float64); ok {
			usage.PromptTokens = int(pt)
		}
		if ct, ok := usageData["completion_tokens"].(float64); ok {
			usage.CompletionTokens = int(ct)
		}
		if tt, ok := usageData["total_tokens"].(float64); ok {
			usage.TotalTokens = int(tt)
		}
		resp.Usage = usage
	}

	return resp
}

// ConvertChatCompletionToResponsesWithAnnotations converts Chat Completions response to Responses format
// with URL citation annotations from grounding metadata
func ConvertChatCompletionToResponsesWithAnnotations(chatResp map[string]interface{}, annotations []mappers.OpenAIAnnotation) OpenAIResponsesResponse {
	// Generate proper response ID (resp_xxx format)
	respID := "resp_" + uuid.New().String()[:12]

	// Get created timestamp
	var createdAt int64
	if created, ok := chatResp["created"].(float64); ok {
		createdAt = int64(created)
	}

	resp := OpenAIResponsesResponse{
		ID:        respID,
		Object:    "response",
		Status:    "completed",
		CreatedAt: createdAt,
	}
	// Safe type assertion with fallback (panic guard)
	if model, ok := chatResp["model"].(string); ok {
		resp.Model = model
	} else {
		resp.Model = "unknown"
	}

	// Convert choices to output items
	chatChoices, ok := chatResp["choices"].([]interface{})
	if !ok || len(chatChoices) == 0 {
		return resp
	}

	resp.Output = make([]ResponsesOutputItem, len(chatChoices))

	for i, choice := range chatChoices {
		chatChoice, ok := choice.(map[string]interface{})
		if !ok {
			continue
		}
		message, ok := chatChoice["message"].(map[string]interface{})
		if !ok {
			continue
		}

		// Handle content safely
		contentText := ""
		if content, ok := message["content"].(string); ok {
			contentText = content
		}

		// Generate item ID
		itemID := "item_" + uuid.New().String()[:8]

		// Get role
		role := "assistant"
		if r, ok := message["role"].(string); ok {
			role = r
		}

		// Create message output item with annotations
		outputContent := ResponsesContent{
			Type:        "output_text",
			Text:        contentText,
			Annotations: annotations, // Include URL citations from grounding
		}

		resp.Output[i] = ResponsesOutputItem{
			ID:      itemID,
			Type:    "message",
			Role:    role,
			Status:  "completed",
			Content: []ResponsesContent{outputContent},
		}
	}

	// Convert usage (with nil check)
	if usageData, ok := chatResp["usage"].(map[string]interface{}); ok && usageData != nil {
		usage := &ResponsesUsage{}
		if pt, ok := usageData["prompt_tokens"].(float64); ok {
			usage.PromptTokens = int(pt)
		}
		if ct, ok := usageData["completion_tokens"].(float64); ok {
			usage.CompletionTokens = int(ct)
		}
		if tt, ok := usageData["total_tokens"].(float64); ok {
			usage.TotalTokens = int(tt)
		}
		resp.Usage = usage
	}

	return resp
}

// OpenAIResponsesHandler handles /v1/responses (OpenAI Responses API)
// Set NEXUS_VERBOSE=1 for detailed request/response logging
func OpenAIResponsesHandler(database *gorm.DB, tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		verbose := IsVerbose()

		// 1. Read and parse request body
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			writeOpenAIError(w, "Failed to read request body", http.StatusBadRequest)
			return
		}

		var req OpenAIResponsesRequest
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			if verbose {
				log.Printf("❌ [VERBOSE] /v1/responses Failed to parse request: %v\nRaw body: %s", err, string(bodyBytes))
			}
			writeOpenAIError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Verbose: Log original request
		if verbose {
			log.Printf("📥 [VERBOSE] /v1/responses Original Request:\n%s", string(bodyBytes))
		}

		// 2. Convert to Chat Completions format
		chatReq := ConvertResponsesToChatCompletion(req)

		if verbose {
			chatReqBytes, _ := json.MarshalIndent(chatReq, "", "  ")
			log.Printf("🔄 [VERBOSE] Converted to Chat Completions format:\n%s", string(chatReqBytes))
		}

		// 3. Apply model routing
		targetModel := db.ResolveModel(chatReq.Model, "google")
		log.Printf("🗺️ /v1/responses Model routing: %s -> %s", chatReq.Model, targetModel)

		// 4. Get token and project ID (support X-Nexus-Account header for account selection)
		var cachedToken *token.CachedToken
		accountEmail := r.Header.Get("X-Nexus-Account")
		if accountEmail != "" {
			cachedToken, err = tokenMgr.GetTokenByIdentifier(accountEmail)
			if err != nil {
				if verbose {
					log.Printf("❌ [VERBOSE] /v1/responses Account not found: %s, error: %v", accountEmail, err)
				}
				writeOpenAIError(w, "Account not found: "+accountEmail, http.StatusUnauthorized)
				return
			}
		} else {
			cachedToken, err = tokenMgr.GetPrimaryOrDefaultToken()
		}
		if err != nil {
			if verbose {
				log.Printf("❌ [VERBOSE] /v1/responses No valid accounts: %v", err)
			}
			writeOpenAIError(w, "No valid accounts available", http.StatusServiceUnavailable)
			return
		}

		// 5. Convert to Gemini format using existing mapper
		geminiPayload := mappers.OpenAIToGemini(chatReq, targetModel, cachedToken.ProjectID)

		// 6. Convert to map and add required Google API fields
		payloadBytes, _ := json.Marshal(geminiPayload)
		var payload map[string]interface{}
		json.Unmarshal(payloadBytes, &payload)

		// Add Cloud Code API required fields
		// Use client-provided X-Request-ID if present, otherwise generate new one
		requestId := r.Header.Get("X-Request-ID")
		if requestId == "" {
			requestId = "agent-" + uuid.New().String()
		}
		payload["userAgent"] = "antigravity"
		payload["requestType"] = "agent"
		payload["requestId"] = requestId

		// Verbose: Log Gemini payload
		if verbose {
			geminiPayloadBytes, _ := json.MarshalIndent(payload, "", "  ")
			log.Printf("📤 [VERBOSE] [%s] Gemini API Request Payload:\n%s", requestId, string(geminiPayloadBytes))
		}

		// 7. Handle streaming vs non-streaming
		if req.Stream {
			// TODO: Implement streaming for Responses API
			handleOpenAIStreaming(w, upstreamClient, cachedToken.AccessToken, payload, targetModel, requestId)
		} else {
			// Non-streaming: Get response from upstream
			resp, err := upstreamClient.GenerateContent(cachedToken.AccessToken, payload)
			if err != nil {
				if verbose {
					log.Printf("❌ [VERBOSE] [%s] /v1/responses Upstream error: %v", requestId, err)
				}
				writeOpenAIError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()

			// Parse Gemini response
			respBodyBytes, _ := io.ReadAll(resp.Body)

			// Handle non-200 responses from Gemini
			if resp.StatusCode != http.StatusOK {
				if verbose {
					var prettyErr map[string]interface{}
					json.Unmarshal(respBodyBytes, &prettyErr)
					prettyErrBytes, _ := json.MarshalIndent(prettyErr, "", "  ")
					log.Printf("❌ [VERBOSE] [%s] /v1/responses Gemini API error (status %d):\n%s", requestId, resp.StatusCode, string(prettyErrBytes))
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(resp.StatusCode)
				w.Write(respBodyBytes)
				return
			}

			var geminiResp map[string]interface{}
			if err := json.Unmarshal(respBodyBytes, &geminiResp); err != nil {
				if verbose {
					log.Printf("❌ [VERBOSE] [%s] /v1/responses Failed to parse Gemini response: %v\nRaw: %s", requestId, err, string(respBodyBytes))
				}
				writeOpenAIError(w, "Failed to parse upstream response", http.StatusInternalServerError)
				return
			}

			// Verbose: Log Gemini response
			if verbose {
				geminiRespBytes, _ := json.MarshalIndent(geminiResp, "", "  ")
				log.Printf("📥 [VERBOSE] [%s] Gemini API Response:\n%s", requestId, string(geminiRespBytes))
			}

			// Convert Gemini response to Chat Completions format
			chatBytes, err := mappers.GeminiToOpenAI(geminiResp, chatReq.Model, false)
			if err != nil {
				if verbose {
					log.Printf("❌ [VERBOSE] [%s] /v1/responses Conversion error: %v", requestId, err)
				}
				writeOpenAIError(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// Extract grounding metadata for annotations (v0.1.6 Google Search feature)
			var annotations []mappers.OpenAIAnnotation
			groundingMetadata := mappers.ExtractGroundingMetadata(geminiResp)
			if groundingMetadata != nil {
				annotations = mappers.ConvertGroundingMetadataToAnnotations(groundingMetadata)
				if verbose && len(annotations) > 0 {
					log.Printf("🔗 [VERBOSE] Extracted %d URL citations from grounding metadata", len(annotations))
				}
			}

			// Parse as map for conversion to Responses format
			var chatCompletionResp map[string]interface{}
			if err := json.Unmarshal(chatBytes, &chatCompletionResp); err != nil {
				if verbose {
					log.Printf("❌ [VERBOSE] /v1/responses Failed to parse chat completion: %v", err)
				}
				writeOpenAIError(w, "Failed to parse chat completion response", http.StatusInternalServerError)
				return
			}

			// Convert Chat Completions to Responses API format
			responsesResp := ConvertChatCompletionToResponsesWithAnnotations(chatCompletionResp, annotations)

			// Verbose: Log final Responses API response
			if verbose {
				finalRespBytes, _ := json.MarshalIndent(responsesResp, "", "  ")
				log.Printf("📤 [VERBOSE] /v1/responses Final Response:\n%s", string(finalRespBytes))
			}

			// Return standard Responses API format (output[] array)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(responsesResp)
		}
	}
}
