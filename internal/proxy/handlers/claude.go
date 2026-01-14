package handlers

import (
	"bufio"
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
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/mappers"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
	"gorm.io/gorm"
)

// ClaudeMessagesHandler handles /anthropic/v1/messages
func ClaudeMessagesHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get token: Check for explicit account header, else use Primary/Default
		var cachedToken *token.CachedToken
		var err error

		if accountHeader := r.Header.Get("X-Nexus-Account"); accountHeader != "" {
			cachedToken, err = tokenMgr.GetTokenByIdentifier(accountHeader)
			if err != nil {
				writeClaudeError(w, fmt.Sprintf("Account not found: %s", accountHeader), http.StatusUnauthorized)
				return
			}
		} else {
			cachedToken, err = tokenMgr.GetPrimaryOrDefaultToken()
			if err != nil {
				writeClaudeError(w, "No valid token available", http.StatusUnauthorized)
				return
			}
		}

		// Parse request as flexible JSON (Claude Code sends complex content blocks)
		var rawReq map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&rawReq); err != nil {
			writeClaudeError(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Extract required fields
		rawModel, _ := rawReq["model"].(string)
		model := db.ResolveModel(rawModel, "google")
		messages, _ := rawReq["messages"].([]interface{})
		stream, _ := rawReq["stream"].(bool)

		log.Printf("📨 Claude request: model=%s messages=%d stream=%v", model, len(messages), stream)

		// Generate requestId early so all logs can use it
		requestId := r.Header.Get("X-Request-ID")
		if requestId == "" {
			requestId = "agent-" + uuid.New().String()
		}

		// Stage 1: Verbose logging for raw Claude request
		if IsVerbose() {
			reqBytes, _ := json.MarshalIndent(rawReq, "", "  ")
			log.Printf("📥 [VERBOSE] [%s] /anthropic/v1/messages Raw request:\n%s", requestId, string(reqBytes))
		}

		// Build Gemini request directly (flexible approach)
		geminiContents := make([]map[string]interface{}, 0)

		// Handle system prompt if present
		if system, ok := rawReq["system"]; ok {
			var systemText string
			switch s := system.(type) {
			case string:
				systemText = s
			case []interface{}:
				// Array of system blocks
				for _, block := range s {
					if b, ok := block.(map[string]interface{}); ok {
						if text, ok := b["text"].(string); ok {
							systemText += text + "\n"
						}
					}
				}
			}
			if systemText != "" {
				geminiContents = append(geminiContents, map[string]interface{}{
					"role": "user",
					"parts": []map[string]interface{}{
						{"text": "[System]: " + systemText},
					},
				})
			}
		}

		// Convert tools
		var geminiTools []map[string]interface{}
		if tools, ok := rawReq["tools"].([]interface{}); ok && len(tools) > 0 {
			var functionDeclarations []map[string]interface{}
			var hasGoogleSearch bool

			for _, t := range tools {
				if toolMap, ok := t.(map[string]interface{}); ok {
					name, _ := toolMap["name"].(string)

					// Web Search Mapping
					// User logic: if NOT gemini-3 AND NOT claude, map "web_search" (or similar) to googleSearch
					isSearchTool := name == "web_search" || name == "google_search"
					supportsSearch := !strings.Contains(model, "gemini-3") && !strings.Contains(model, "claude") && !strings.Contains(model, "gemini-3-pro")

					if isSearchTool {
						if supportsSearch {
							hasGoogleSearch = true
							log.Printf("🔍 [ClaudeHandler] Mapping '%s' to googleSearch for model %s", name, model)
						} else {
							log.Printf("⚠️ [ClaudeHandler] Skipping '%s' for model %s (search not supported)", name, model)
						}
						continue
					}

					// Function Tool Mapping
					description, _ := toolMap["description"].(string)
					inputSchema, _ := toolMap["input_schema"].(map[string]interface{})

					// Clean the schema for Gemini compatibility
					cleanedSchema := cleanSchemaForGemini(inputSchema)

					functionDeclarations = append(functionDeclarations, map[string]interface{}{
						"name":        name,
						"description": description,
						"parameters":  cleanedSchema,
					})
				}
			}

			if len(functionDeclarations) > 0 {
				geminiTools = append(geminiTools, map[string]interface{}{
					"functionDeclarations": functionDeclarations,
				})
			}
			if hasGoogleSearch {
				geminiTools = append(geminiTools, map[string]interface{}{
					"googleSearch": map[string]interface{}{},
				})
			}
		}

		// Convert messages
		for _, msg := range messages {
			m, ok := msg.(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			if role == "assistant" {
				role = "model"
			}

			var parts []map[string]interface{}
			content := m["content"]
			switch c := content.(type) {
			case string:
				parts = append(parts, map[string]interface{}{"text": c})
			case []interface{}:
				// Array of content blocks
				for _, block := range c {
					if b, ok := block.(map[string]interface{}); ok {
						blockType, _ := b["type"].(string)
						switch blockType {
						case "text":
							if text, ok := b["text"].(string); ok {
								parts = append(parts, map[string]interface{}{"text": text})
							}
						case "thinking":
							if thinking, ok := b["thinking"].(string); ok {
								parts = append(parts, map[string]interface{}{
									"text":    thinking,
									"thought": true,
								})
							}
						case "tool_use":
							// Convert to Gemini function call
							// Gemini-3 models require thoughtSignature, use skip sentinel
							name, _ := b["name"].(string)
							id, _ := b["id"].(string)
							input := b["input"]
							parts = append(parts, map[string]interface{}{
								"thoughtSignature": "skip_thought_signature_validator",
								"functionCall": map[string]interface{}{
									"id":   id,
									"name": name,
									"args": input,
								},
							})
						case "tool_result":
							// Convert to Gemini function response
							toolUseId, _ := b["tool_use_id"].(string)
							resultContent := b["content"]
							// Parse content if it's a string (JSON) - Gemini requires Struct
							var responseResult interface{}
							switch c := resultContent.(type) {
							case string:
								var parsed map[string]interface{}
								if err := json.Unmarshal([]byte(c), &parsed); err == nil {
									responseResult = parsed
								} else {
									responseResult = c
								}
							case map[string]interface{}:
								responseResult = c
							default:
								responseResult = fmt.Sprintf("%v", resultContent)
							}

							parts = append(parts, map[string]interface{}{
								"functionResponse": map[string]interface{}{
									"id":   toolUseId,
									"name": toolUseId,
									"response": map[string]interface{}{
										"result": responseResult,
									},
								},
							})
						}
					}
				}
			}
			if len(parts) > 0 {
				geminiContents = append(geminiContents, map[string]interface{}{
					"role":  role,
					"parts": parts,
				})
			}
		}

		// Generate sessionId per oh-my-opencode reference: "-{random_number}"
		sessionId := fmt.Sprintf("-%d", time.Now().UnixNano())

		// Build request object
		requestObj := map[string]interface{}{
			"contents":  geminiContents,
			"sessionId": sessionId, // Required by oh-my-opencode for multi-turn conversations
			"generationConfig": map[string]interface{}{
				"maxOutputTokens": 64000,
			},
		}

		// Only add tools and toolConfig if geminiTools is not empty
		if len(geminiTools) > 0 {
			requestObj["tools"] = geminiTools
			requestObj["toolConfig"] = map[string]interface{}{
				"functionCallingConfig": map[string]interface{}{
					"mode": "AUTO", // Use AUTO mode for flexibility
				},
			}
		}

		payload := map[string]interface{}{
			"project":     cachedToken.ProjectID,
			"requestId":   requestId,
			"model":       model,
			"userAgent":   "antigravity",
			"requestType": "agent", // Restored per Antigravity-Manager reference
			"request":     requestObj,
		}

		// Verbose: Log Gemini payload before sending
		if IsVerbose() {
			geminiPayloadBytes, _ := json.MarshalIndent(payload, "", "  ")
			log.Printf("📤 [VERBOSE] [%s] /anthropic/v1/messages Gemini Request Payload:\n%s", requestId, string(geminiPayloadBytes))
		}

		if stream {
			handleClaudeStreaming(w, upstreamClient, cachedToken.AccessToken, payload, model, requestId)
		} else {
			handleClaudeNonStreaming(w, upstreamClient, cachedToken.AccessToken, payload, model, requestId)
		}
	}
}

func handleClaudeNonStreaming(w http.ResponseWriter, client *upstream.Client, token string, payload map[string]interface{}, model string, requestId string) {
	// Use SmartGenerateContent for automatic premium model handling
	resp, err := client.SmartGenerateContent(token, payload)
	if err != nil {
		if IsVerbose() {
			log.Printf("❌ [VERBOSE] [%s] /anthropic/v1/messages Upstream error: %v", requestId, err)
		}
		writeClaudeError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		if IsVerbose() {
			var prettyErr map[string]interface{}
			json.Unmarshal(body, &prettyErr)
			prettyBytes, _ := json.MarshalIndent(prettyErr, "", "  ")
			log.Printf("❌ [VERBOSE] [%s] /anthropic/v1/messages Gemini API error (status %d):\n%s", requestId, resp.StatusCode, string(prettyBytes))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
		return
	}

	// Unwrap Cloud Code API response
	var wrapped map[string]interface{}
	json.Unmarshal(body, &wrapped)

	geminiResp, ok := wrapped["response"].(map[string]interface{})
	if !ok {
		json.Unmarshal(body, &geminiResp)
	}

	// Stage 3: Verbose logging for Gemini response
	if IsVerbose() {
		prettyBytes, _ := json.MarshalIndent(wrapped, "", "  ")
		log.Printf("📥 [VERBOSE] [%s] Gemini API Response:\n%s", requestId, string(prettyBytes))
	}

	claudeResp, err := mappers.GeminiToClaude(geminiResp, model)
	if err != nil {
		if IsVerbose() {
			log.Printf("❌ [VERBOSE] [%s] /anthropic/v1/messages Conversion error: %v", requestId, err)
		}
		writeClaudeError(w, "Response conversion error", http.StatusInternalServerError)
		return
	}

	// Stage 4: Verbose logging for final Claude response
	if IsVerbose() {
		log.Printf("📤 [VERBOSE] [%s] /anthropic/v1/messages Final Response:\n%s", requestId, string(claudeResp))
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(claudeResp)
}

func handleClaudeStreaming(w http.ResponseWriter, client *upstream.Client, token string, payload map[string]interface{}, model string, requestId string) {
	// Use SmartStreamGenerateContent for automatic premium model handling
	resp, err := client.SmartStreamGenerateContent(token, payload)
	if err != nil {
		writeClaudeError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Check upstream status before switching to SSE (streaming reliability fix)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if IsVerbose() {
			log.Printf("❌ [VERBOSE] /anthropic/v1/messages Streaming upstream error (status %d):\n%s", resp.StatusCode, string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeClaudeError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send message_start event
	msgStart := &mappers.ClaudeResponse{
		ID:      "msg-nexus",
		Type:    "message",
		Role:    "assistant",
		Model:   model,
		Content: []mappers.ClaudeContentBlock{},
		Usage:   mappers.ClaudeUsage{InputTokens: 0, OutputTokens: 0},
	}
	startEvent, _ := mappers.CreateClaudeStreamEvent("message_start", msgStart)
	fmt.Fprintf(w, "event: message_start\ndata: %s\n\n", startEvent)
	flusher.Flush()

	// Track whether we've started a text block (defer until we have actual text)
	textBlockStarted := false
	hasAnyContent := false

	// Increase scanner buffer to handle large SSE frames (8MB limit)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	chunkCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			// Verbose: log raw streaming chunk
			if IsVerbose() {
				log.Printf("📦 [VERBOSE] [%s] /anthropic/v1/messages Stream chunk #%d: %s", requestId, chunkCount+1, data)
			}

			// Parse and unwrap response field
			var wrapped map[string]interface{}
			if err := json.Unmarshal([]byte(data), &wrapped); err != nil {
				if IsVerbose() {
					log.Printf("⚠️ [VERBOSE] [%s] /anthropic/v1/messages Stream parse error: %v", requestId, err)
				}
				continue
			}

			geminiResp, ok := wrapped["response"].(map[string]interface{})
			if !ok {
				json.Unmarshal([]byte(data), &geminiResp)
			}

			// Extract text
			text := extractTextFromGemini(geminiResp)
			if text != "" {
				// Start text block on first actual text
				if !textBlockStarted {
					fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
					flusher.Flush()
					textBlockStarted = true
				}
				hasAnyContent = true
				delta := &mappers.ClaudeDelta{Type: "text_delta", Text: text}
				deltaEvent, _ := mappers.CreateClaudeStreamEvent("content_block_delta", delta)
				fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", deltaEvent)
				flusher.Flush()
				chunkCount++
			}

			// Extract functionCall (tool_use) for streaming
			if funcCall := extractFunctionCallFromGemini(geminiResp); funcCall != nil {
				// Close text block if it was started
				if textBlockStarted {
					fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
					flusher.Flush()
				}

				// Determine tool_use index (0 if no text, 1 if text was present)
				toolIndex := 0
				if textBlockStarted {
					toolIndex = 1
				}
				hasAnyContent = true

				// Send tool_use content_block_start
				toolUseStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"%s","name":"%s","input":{}}}`,
					toolIndex, funcCall["id"], funcCall["name"])
				fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n", toolUseStart)
				flusher.Flush()

				// Send input delta - partial_json should be a string, not embedded JSON
				if inputBytes, err := json.Marshal(funcCall["args"]); err == nil {
					// Escape the JSON string for embedding in another JSON
					escapedInput, _ := json.Marshal(string(inputBytes))
					inputDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%s}}`,
						toolIndex, string(escapedInput))
					fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", inputDelta)
					flusher.Flush()
				}

				// Send content_block_stop for tool_use
				fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", toolIndex)
				flusher.Flush()
				chunkCount++
			}
		}
	}
	// Check scanner error after loop (streaming reliability fix)
	if err := scanner.Err(); err != nil && IsVerbose() {
		log.Printf("❌ [VERBOSE] [%s] /anthropic/v1/messages Scanner error: %v", requestId, err)
	}
	// Summary log for diagnosing empty responses
	if IsVerbose() {
		if chunkCount == 0 {
			log.Printf("⚠️ [VERBOSE] [%s] /anthropic/v1/messages Streaming completed with 0 chunks - client received empty response!", requestId)
		} else {
			log.Printf("✅ [VERBOSE] [%s] /anthropic/v1/messages Streaming completed: %d chunks sent", requestId, chunkCount)
		}
	}

	// Only send text block stop if we started one and haven't closed it
	if textBlockStarted {
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		flusher.Flush()
	}

	// Determine stop_reason based on content
	stopReason := "end_turn"
	if !textBlockStarted && hasAnyContent {
		// Only tool_use was sent
		stopReason = "tool_use"
	}

	// message_delta with usage (required by Anthropic SDK)
	fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"%s\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":0}}\n\n", stopReason)
	flusher.Flush()

	fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	flusher.Flush()
}

func extractTextFromGemini(resp map[string]interface{}) string {
	if candidates, ok := resp["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := candidate["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
					if part, ok := parts[0].(map[string]interface{}); ok {
						if t, ok := part["text"].(string); ok {
							return t
						}
					}
				}
			}
		}
	}
	return ""
}

// extractFunctionCallFromGemini extracts functionCall from Gemini streaming response
func extractFunctionCallFromGemini(resp map[string]interface{}) map[string]interface{} {
	if candidates, ok := resp["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := candidate["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok {
					for _, part := range parts {
						if p, ok := part.(map[string]interface{}); ok {
							if funcCall, ok := p["functionCall"].(map[string]interface{}); ok {
								name, _ := funcCall["name"].(string)
								args := funcCall["args"]
								id, _ := funcCall["id"].(string)
								if id == "" {
									// Generate ID if not present
									id = "toolu_" + time.Now().Format("20060102150405")
								}
								return map[string]interface{}{
									"id":   id,
									"name": name,
									"args": args,
								}
							}
						}
					}
				}
			}
		}
	}
	return nil
}

func writeClaudeError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"type": "error",
		"error": map[string]interface{}{
			"type":    "api_error",
			"message": message,
		},
	})
}

// ClaudeModelsHandler handles /anthropic/v1/models (GET)
// Returns models declared in config that have active routes
func ClaudeModelsHandler(database *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Get declared models from config
		declaredModels, err := db.GetConfigModels(database, "anthropic_models")
		if err != nil {
			log.Printf("⚠️ Failed to load anthropic_models from config: %v", err)
			writeClaudeError(w, "Failed to load models", http.StatusInternalServerError)
			return
		}

		// 2. Get set of client models that have active routes
		routedModels := db.GetClientModelsSet(database)

		// 3. Filter and convert to Anthropic format
		var validModels []map[string]interface{}
		for _, model := range declaredModels {
			modelID, ok := model["id"].(string)
			if ok && routedModels[modelID] {
				// Convert to Anthropic API format
				anthropicModel := map[string]interface{}{
					"type":         "model",
					"id":           modelID,
					"display_name": model["display_name"],
					"created_at":   model["created"],
				}
				validModels = append(validModels, anthropicModel)
			}
		}

		// 4. Return Anthropic-compatible response
		response := map[string]interface{}{
			"data":     validModels,
			"has_more": false,
		}

		if len(validModels) > 0 {
			response["first_id"] = validModels[0]["id"]
			response["last_id"] = validModels[len(validModels)-1]["id"]
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}
}

// ClaudeMessagesHandlerWithMonitor wraps ClaudeMessagesHandler with request logging
func ClaudeMessagesHandlerWithMonitor(tokenMgr *token.Manager, upstreamClient *upstream.Client, pm *monitor.ProxyMonitor) http.HandlerFunc {
	baseHandler := ClaudeMessagesHandler(tokenMgr, upstreamClient)

	return func(w http.ResponseWriter, r *http.Request) {
		if !pm.IsEnabled() {
			baseHandler(w, r)
			return
		}

		startTime := time.Now()

		// Read and restore body for logging
		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

		// Extract model from request
		var req struct {
			Model string `json:"model"`
		}
		json.Unmarshal(bodyBytes, &req)

		// Get account email
		var accountEmail string
		if accountHeader := r.Header.Get("X-Nexus-Account"); accountHeader != "" {
			if cachedToken, err := tokenMgr.GetTokenByIdentifier(accountHeader); err == nil {
				accountEmail = cachedToken.Email
			}
		} else {
			if cachedToken, err := tokenMgr.GetPrimaryOrDefaultToken(); err == nil {
				accountEmail = cachedToken.Email
			}
		}

		// Use response recorder
		rec := &responseRecorder{ResponseWriter: w, statusCode: 200}

		baseHandler(rec, r)

		// Extract tokens and error from response
		var inputTokens, outputTokens int
		var errorMsg string
		respBody := rec.body.String()

		if rec.statusCode >= 200 && rec.statusCode < 400 {
			// Parse usage from Claude response
			var resp struct {
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(respBody), &resp) == nil {
				inputTokens = resp.Usage.InputTokens
				outputTokens = resp.Usage.OutputTokens
			}
		} else {
			// Extract error message
			var errResp struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(respBody), &errResp) == nil && errResp.Error.Message != "" {
				errorMsg = errResp.Error.Message
			} else if len(respBody) < 500 {
				errorMsg = respBody
			}
		}

		// Log the request
		pm.LogRequest(models.RequestLog{
			Method:       r.Method,
			URL:          r.URL.Path,
			Status:       rec.statusCode,
			Duration:     time.Since(startTime).Milliseconds(),
			Model:        req.Model,
			MappedModel:  db.ResolveModel(req.Model, "google"),
			AccountEmail: accountEmail,
			Error:        errorMsg,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			RequestBody:  string(bodyBytes),
			ResponseBody: respBody,
		})
	}
}

// cleanSchemaForGemini recursively removes JSON Schema fields not supported by Gemini API.
// Gemini doesn't support: $schema, exclusiveMinimum, exclusiveMaximum, and some other JSON Schema 7 fields.
func cleanSchemaForGemini(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}

	// Create a copy to avoid modifying the original
	cleaned := make(map[string]interface{})

	// Fields not supported by Gemini API
	unsupportedFields := map[string]bool{
		"$schema":           true,
		"exclusiveMinimum":  true,
		"exclusiveMaximum":  true,
		"$id":               true,
		"$ref":              true,
		"$defs":             true,
		"definitions":       true,
		"patternProperties": true,
		"additionalItems":   true,
		"contains":          true,
		"propertyNames":     true,
		"if":                true,
		"then":              true,
		"else":              true,
		"allOf":             true,
		"anyOf":             true,
		"oneOf":             true,
		"not":               true,
	}

	for key, value := range schema {
		// Skip unsupported fields
		if unsupportedFields[key] {
			// Convert exclusiveMinimum to minimum if it's a number
			if key == "exclusiveMinimum" {
				if num, ok := value.(float64); ok {
					cleaned["minimum"] = num + 1
				} else if num, ok := value.(int); ok {
					cleaned["minimum"] = num + 1
				}
			}
			// Convert exclusiveMaximum to maximum if it's a number
			if key == "exclusiveMaximum" {
				if num, ok := value.(float64); ok {
					cleaned["maximum"] = num - 1
				} else if num, ok := value.(int); ok {
					cleaned["maximum"] = num - 1
				}
			}
			continue
		}

		// Recursively clean nested objects
		switch v := value.(type) {
		case map[string]interface{}:
			cleaned[key] = cleanSchemaForGemini(v)
		case []interface{}:
			// Handle arrays (e.g., items in arrays, required fields)
			cleanedArray := make([]interface{}, len(v))
			for i, item := range v {
				if itemMap, ok := item.(map[string]interface{}); ok {
					cleanedArray[i] = cleanSchemaForGemini(itemMap)
				} else {
					cleanedArray[i] = item
				}
			}
			cleaned[key] = cleanedArray
		default:
			cleaned[key] = value
		}
	}

	return cleaned
}
