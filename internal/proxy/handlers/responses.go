package handlers

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

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
	ID                 string                 `json:"id"`         // "resp_xxx" format
	Object             string                 `json:"object"`     // "response"
	Status             string                 `json:"status"`     // "completed", "in_progress", "failed"
	CreatedAt          int64                  `json:"created_at"` // Unix timestamp
	Model              string                 `json:"model"`
	Output             []ResponsesOutputItem  `json:"output"`
	PreviousResponseID string                 `json:"previous_response_id,omitempty"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
	Usage              *ResponsesUsage        `json:"usage,omitempty"`
	Error              *ResponsesError        `json:"error,omitempty"`
}

type responsesCompatContext struct {
	Conversation       string
	PreviousResponseID string
}

const responsesCompatRequestIDSeparator = "__ctx__"

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
		return responsesContentItemsToText(contentItems)
	}

	// Fallback parser to support flexible input_image.image_url object format.
	var rawItems []map[string]interface{}
	if err := json.Unmarshal(content, &rawItems); err == nil {
		var textParts []string
		for _, item := range rawItems {
			itemType, _ := item["type"].(string)
			switch itemType {
			case "input_text", "text", "output_text":
				if text, ok := item["text"].(string); ok && strings.TrimSpace(text) != "" {
					textParts = append(textParts, text)
				}
			case "input_image":
				if imageURL, ok := extractInputImageURL(item["image_url"]); ok {
					textParts = append(textParts, "[input_image] "+imageURL)
				}
			case "input_file":
				if fileID, ok := item["file_id"].(string); ok && strings.TrimSpace(fileID) != "" {
					textParts = append(textParts, "[input_file] "+fileID)
				}
			}
		}
		return strings.Join(textParts, "\n")
	}

	return ""
}

func extractInputImageURL(v interface{}) (string, bool) {
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
		return s, true
	}
	if obj, ok := v.(map[string]interface{}); ok {
		if u, ok := obj["url"].(string); ok && strings.TrimSpace(u) != "" {
			return u, true
		}
	}
	return "", false
}

func responsesContentItemsToText(contentItems []ResponsesContent) string {
	var textParts []string
	for _, item := range contentItems {
		switch item.Type {
		case "input_text", "text", "output_text":
			if strings.TrimSpace(item.Text) != "" {
				textParts = append(textParts, item.Text)
			}
		case "input_image":
			if strings.TrimSpace(item.ImageURL) != "" {
				textParts = append(textParts, "[input_image] "+item.ImageURL)
			}
		case "input_file":
			if strings.TrimSpace(item.FileID) != "" {
				textParts = append(textParts, "[input_file] "+item.FileID)
			}
		}
	}
	return strings.Join(textParts, "\n")
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
		requestId := GetOrGenerateRequestID(r)

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

		// 2. Check provider routing - Codex models use passthrough
		targetModel, provider := db.ResolveModelWithProvider(req.Model)
		log.Printf("🗺️ /v1/responses Model routing: %s -> %s (provider: %s)", req.Model, targetModel, provider)

		if provider == "codex" {
			// Codex: direct passthrough - no conversion needed
			handleCodexResponsesPassthrough(w, bodyBytes, targetModel, requestId)
			return
		}

		// Google Cloud Code flow (existing behavior)
		// 2. Convert to Chat Completions format
		chatReq := ConvertResponsesToChatCompletion(req)

		if verbose {
			chatReqBytes, _ := json.MarshalIndent(chatReq, "", "  ")
			log.Printf("🔄 [VERBOSE] Converted to Chat Completions format:\n%s", string(chatReqBytes))
		}

		// 3. Apply model routing for Google flow (already resolved above)
		log.Printf("🗺️ /v1/responses Google flow: %s -> %s", chatReq.Model, targetModel)

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

		// Add Cloud Code API required fields and keep compatibility metadata explicit.
		compatCtx, smuggled := applyResponsesUpstreamFields(payload, requestId, req)
		if smuggled {
			w.Header().Set("X-Nexus-Responses-Compat", "request_id_smuggled")
			if verbose {
				log.Printf("ℹ️ [%s] /v1/responses Encoded compatibility fields into requestId", requestId)
			}
		}

		// Verbose: Log Gemini payload
		if verbose {
			geminiPayloadBytes, _ := json.MarshalIndent(payload, "", "  ")
			log.Printf("📤 [VERBOSE] [%s] Gemini API Request Payload:\n%s", requestId, string(geminiPayloadBytes))
		}

		// 7. Handle streaming vs non-streaming
		if req.Stream {
			handleResponsesStreaming(w, upstreamClient, cachedToken.AccessToken, payload, chatReq.Model, requestId, compatCtx)
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
			applyResponsesCompatToResponse(&responsesResp, compatCtx)

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

func handleResponsesStreaming(w http.ResponseWriter, client *upstream.Client, token string, payload map[string]interface{}, model string, requestId string, compatCtx responsesCompatContext) {
	resp, err := client.SmartStreamGenerateContent(token, payload)
	if err != nil {
		writeOpenAIError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	SetSSEHeaders(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	respID := "resp_" + uuid.New().String()[:12]
	itemID := "item_" + uuid.New().String()[:8]
	created := time.Now().Unix()
	fullText := strings.Builder{}
	var latestUsage *ResponsesUsage

	createdEvent := map[string]interface{}{
		"type": "response.created",
		"response": map[string]interface{}{
			"id":         respID,
			"object":     "response",
			"status":     "in_progress",
			"created_at": created,
			"model":      model,
		},
	}
	applyResponsesCompatToMap(createdEvent["response"].(map[string]interface{}), compatCtx)
	if b, err := json.Marshal(createdEvent); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	safetyChecker := NewStreamSafetyChecker()

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		if abort, reason := safetyChecker.CheckChunk([]byte(data)); abort {
			log.Printf("⚠️ [%s] /v1/responses Stream aborted by safety checker: %s", requestId, reason)
			break
		}

		var wrapped map[string]interface{}
		if err := json.Unmarshal([]byte(data), &wrapped); err != nil {
			continue
		}

		geminiResp, ok := wrapped["response"].(map[string]interface{})
		if !ok {
			geminiResp = wrapped
		}

		text := extractTextFromGemini(geminiResp)
		if text != "" {
			fullText.WriteString(text)
			deltaEvent := map[string]interface{}{
				"type":          "response.output_text.delta",
				"response_id":   respID,
				"item_id":       itemID,
				"output_index":  0,
				"content_index": 0,
				"delta":         text,
			}
			if b, err := json.Marshal(deltaEvent); err == nil {
				fmt.Fprintf(w, "data: %s\n\n", b)
				flusher.Flush()
			}
		}

		if usage := extractResponsesUsageFromGemini(geminiResp); usage != nil {
			latestUsage = usage
		}
	}

	outputDoneEvent := map[string]interface{}{
		"type":          "response.output_text.done",
		"response_id":   respID,
		"item_id":       itemID,
		"output_index":  0,
		"content_index": 0,
		"text":          fullText.String(),
	}
	if b, err := json.Marshal(outputDoneEvent); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	completedResponse := map[string]interface{}{
		"id":         respID,
		"object":     "response",
		"status":     "completed",
		"created_at": created,
		"model":      model,
		"output": []map[string]interface{}{
			{
				"id":     itemID,
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]interface{}{
					{
						"type": "output_text",
						"text": fullText.String(),
					},
				},
			},
		},
	}
	if latestUsage != nil {
		completedResponse["usage"] = latestUsage
	}
	applyResponsesCompatToMap(completedResponse, compatCtx)

	completedEvent := map[string]interface{}{
		"type":     "response.completed",
		"response": completedResponse,
	}
	if b, err := json.Marshal(completedEvent); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func extractResponsesUsageFromGemini(geminiResp map[string]interface{}) *ResponsesUsage {
	usageMeta, ok := geminiResp["usageMetadata"].(map[string]interface{})
	if !ok {
		return nil
	}
	usage := &ResponsesUsage{}
	if v, ok := usageMeta["promptTokenCount"].(float64); ok {
		usage.PromptTokens = int(v)
	}
	if v, ok := usageMeta["candidatesTokenCount"].(float64); ok {
		usage.CompletionTokens = int(v)
	}
	if v, ok := usageMeta["totalTokenCount"].(float64); ok {
		usage.TotalTokens = int(v)
	} else {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return usage
}

func applyResponsesUpstreamFields(payload map[string]interface{}, requestID string, req OpenAIResponsesRequest) (responsesCompatContext, bool) {
	payload["userAgent"] = "antigravity"
	payload["requestType"] = "agent"

	compatCtx := responsesCompatContext{
		Conversation:       strings.TrimSpace(req.Conversation),
		PreviousResponseID: strings.TrimSpace(req.PreviousResponseID),
	}
	encodedRequestID, encoded := encodeResponsesCompatRequestID(requestID, compatCtx)
	payload["requestId"] = encodedRequestID

	return compatCtx, encoded
}

func encodeResponsesCompatRequestID(baseRequestID string, compatCtx responsesCompatContext) (string, bool) {
	if compatCtx.Conversation == "" && compatCtx.PreviousResponseID == "" {
		return baseRequestID, false
	}

	raw := map[string]string{
		"c": compatCtx.Conversation,
		"p": compatCtx.PreviousResponseID,
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return baseRequestID, false
	}

	encoded := base64.RawURLEncoding.EncodeToString(body)
	smuggled := baseRequestID + responsesCompatRequestIDSeparator + encoded
	// Keep requestId bounded for upstream compatibility.
	if len(smuggled) > 240 {
		return baseRequestID, false
	}
	return smuggled, true
}

func decodeResponsesCompatRequestID(requestID string) responsesCompatContext {
	parts := strings.SplitN(requestID, responsesCompatRequestIDSeparator, 2)
	if len(parts) != 2 {
		return responsesCompatContext{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return responsesCompatContext{}
	}
	var decoded map[string]string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return responsesCompatContext{}
	}
	return responsesCompatContext{
		Conversation:       strings.TrimSpace(decoded["c"]),
		PreviousResponseID: strings.TrimSpace(decoded["p"]),
	}
}

func applyResponsesCompatToResponse(resp *OpenAIResponsesResponse, compatCtx responsesCompatContext) {
	if resp == nil {
		return
	}
	if compatCtx.PreviousResponseID != "" {
		resp.PreviousResponseID = compatCtx.PreviousResponseID
	}
	if compatCtx.Conversation != "" {
		if resp.Metadata == nil {
			resp.Metadata = make(map[string]interface{})
		}
		resp.Metadata["conversation"] = compatCtx.Conversation
	}
}

func applyResponsesCompatToMap(resp map[string]interface{}, compatCtx responsesCompatContext) {
	if resp == nil {
		return
	}
	if compatCtx.PreviousResponseID != "" {
		resp["previous_response_id"] = compatCtx.PreviousResponseID
	}
	if compatCtx.Conversation != "" {
		meta, _ := resp["metadata"].(map[string]interface{})
		if meta == nil {
			meta = make(map[string]interface{})
		}
		meta["conversation"] = compatCtx.Conversation
		resp["metadata"] = meta
	}
}
