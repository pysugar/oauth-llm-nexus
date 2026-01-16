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

	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/mappers"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
	"github.com/pysugar/oauth-llm-nexus/internal/util"
	"gorm.io/gorm"
)

// ClaudeMessagesHandler handles /anthropic/v1/messages
func ClaudeMessagesHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get token using common helper
		cachedToken, err := GetTokenFromRequest(r, tokenMgr)
		if err != nil {
			writeClaudeError(w, "No valid token available: "+err.Error(), http.StatusUnauthorized)
			return
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

		// Normalize messages: split mixed tool_result + text messages into separate messages
		// Anthropic API requires tool_result messages to not mix with other content types
		originalLen := len(messages)
		messages = normalizeClaudeMessages(messages)
		if len(messages) != originalLen {
			log.Printf("🔧 [%s] Messages normalized: %d -> %d", rawModel, originalLen, len(messages))
		}

		log.Printf("📨 Claude request: model=%s messages=%d stream=%v", model, len(messages), stream)

		// Generate requestId using common helper
		requestId := GetOrGenerateRequestID(r)

		// Stage 1: Verbose logging for raw Claude request
		if IsVerbose() {
			reqBytes, _ := json.MarshalIndent(rawReq, "", "  ")
			log.Printf("📥 [VERBOSE] [%s] /anthropic/v1/messages Raw request:\n%s", requestId, util.TruncateBytes(reqBytes))
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
			geminiRole := role
			if role == "assistant" {
				geminiRole = "model"
			}

			// Separate parts by type for proper ordering:
			// - For model/assistant: text/thinking FIRST, then functionCall LAST
			// - For user: functionResponse in SEPARATE message from text
			// This is critical for Cloud Code API's Claude backend validation
			var functionCallParts []map[string]interface{}
			var functionResponseParts []map[string]interface{}
			var textParts []map[string]interface{}

			content := m["content"]
			switch c := content.(type) {
			case string:
				textParts = append(textParts, map[string]interface{}{"text": c})
			case []interface{}:
				// Array of content blocks
				for _, block := range c {
					if b, ok := block.(map[string]interface{}); ok {
						blockType, _ := b["type"].(string)
						switch blockType {
						case "text":
							if text, ok := b["text"].(string); ok {
								textParts = append(textParts, map[string]interface{}{"text": text})
							}
						case "thinking":
							if thinking, ok := b["thinking"].(string); ok {
								textParts = append(textParts, map[string]interface{}{
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
							functionCallParts = append(functionCallParts, map[string]interface{}{
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
							// Extract function name from tool_use_id using stateless parsing
							// Format: {funcName}-{random8hex}
							funcName := mappers.ExtractFunctionName(toolUseId)
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

							functionResponseParts = append(functionResponseParts, map[string]interface{}{
								"functionResponse": map[string]interface{}{
									"id":   toolUseId,
									"name": funcName,
									"response": map[string]interface{}{
										"result": responseResult,
									},
								},
							})
						}
					}
				}
			}

			// Build Gemini messages based on role and content types
			if role == "assistant" {
				// For model/assistant messages: text/thinking FIRST, then functionCall LAST
				// This ensures Cloud Code API can correctly map tool_use -> tool_result
				allParts := append(textParts, functionCallParts...)
				if len(allParts) > 0 {
					geminiContents = append(geminiContents, map[string]interface{}{
						"role":  geminiRole,
						"parts": allParts,
					})
				}
				if len(functionCallParts) > 0 && len(textParts) > 0 {
					log.Printf("🔧 Reordered model message: %d text + %d functionCall (functionCall last)",
						len(textParts), len(functionCallParts))
				}
			} else if role == "user" && len(functionResponseParts) > 0 && len(textParts) > 0 {
				// For user messages with mixed functionResponse and text:
				// Split into separate messages: functionResponse FIRST, then text
				geminiContents = append(geminiContents, map[string]interface{}{
					"role":  geminiRole,
					"parts": functionResponseParts,
				})
				geminiContents = append(geminiContents, map[string]interface{}{
					"role":  geminiRole,
					"parts": textParts,
				})
				log.Printf("🔧 Split user message: %d functionResponse + %d text",
					len(functionResponseParts), len(textParts))
			} else {
				// Normal case: combine all parts
				allParts := append(append(functionResponseParts, textParts...), functionCallParts...)
				if len(allParts) > 0 {
					geminiContents = append(geminiContents, map[string]interface{}{
						"role":  geminiRole,
						"parts": allParts,
					})
				}
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
			log.Printf("📤 [VERBOSE] [%s] /anthropic/v1/messages Gemini Request Payload:\n%s", requestId, util.TruncateBytes(geminiPayloadBytes))
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

	body, err := io.ReadAll(resp.Body)
	if err != nil && IsVerbose() {
		log.Printf("⚠️ [VERBOSE] [%s] /anthropic/v1/messages ReadAll error: %v", requestId, err)
	}

	if resp.StatusCode != http.StatusOK {
		if IsVerbose() {
			var prettyErr map[string]interface{}
			json.Unmarshal(body, &prettyErr)
			prettyBytes, _ := json.MarshalIndent(prettyErr, "", "  ")
			log.Printf("❌ [VERBOSE] [%s] /anthropic/v1/messages Gemini API error (status %d):\n%s", requestId, resp.StatusCode, util.TruncateBytes(prettyBytes))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
		return
	}

	// Unwrap Cloud Code API response
	var wrapped map[string]interface{}
	if err := json.Unmarshal(body, &wrapped); err != nil && IsVerbose() {
		log.Printf("⚠️ [VERBOSE] [%s] /anthropic/v1/messages Unmarshal error: %v", requestId, err)
	}

	geminiResp, ok := wrapped["response"].(map[string]interface{})
	if !ok {
		json.Unmarshal(body, &geminiResp)
	}

	// Stage 3: Verbose logging for Gemini response
	if IsVerbose() {
		prettyBytes, _ := json.MarshalIndent(wrapped, "", "  ")
		log.Printf("📥 [VERBOSE] [%s] Gemini API Response:\n%s", requestId, util.TruncateBytes(prettyBytes))
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
		log.Printf("📤 [VERBOSE] [%s] /anthropic/v1/messages Final Response:\n%s", requestId, util.TruncateBytes(claudeResp))
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
			log.Printf("❌ [VERBOSE] /anthropic/v1/messages Streaming upstream error (status %d):\n%s", resp.StatusCode, util.TruncateBytes(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
		return
	}

	SetSSEHeaders(w)

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

	// Track content blocks with proper indexing (inspired by CLIProxyAPI's ResponseIndex)
	contentIndex := 0         // Increments for each content block
	textBlockStarted := false // Whether current text block is open
	hasToolUse := false       // Whether any tool_use was sent (for stop_reason)

	// Tool call observability: track IDs for summary log
	toolUseCount := 0
	toolUseIDs := []string{}

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

			// Verbose: log raw streaming chunk (truncated for large chunks)
			if IsVerbose() {
				log.Printf("📦 [VERBOSE] [%s] /anthropic/v1/messages Stream chunk #%d: %s", requestId, chunkCount+1, util.TruncateLog(data, 512))
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
					fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n", contentIndex)
					flusher.Flush()
					textBlockStarted = true
				}
				delta := &mappers.ClaudeDelta{Type: "text_delta", Text: text}
				deltaEvent, _ := mappers.CreateClaudeStreamEvent("content_block_delta", delta)
				fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", deltaEvent)
				flusher.Flush()
				chunkCount++
			}

			// Extract functionCall (tool_use) for streaming
			if funcCall := extractFunctionCallFromGemini(geminiResp); funcCall != nil {
				// Close text block if it was started (BEFORE determining toolIndex)
				if textBlockStarted {
					fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", contentIndex)
					flusher.Flush()
					contentIndex++ // Increment AFTER closing text block
					textBlockStarted = false
				}

				hasToolUse = true
				toolIndex := contentIndex // Use current contentIndex for tool_use

				// Tool call observability: track IDs
				toolUseCount++
				if id, ok := funcCall["id"].(string); ok {
					toolUseIDs = append(toolUseIDs, id)
				}

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
				contentIndex++ // Increment after closing tool_use block
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
		// Tool call summary for observability
		if toolUseCount > 0 {
			log.Printf("🔧 [VERBOSE] [%s] Tool call summary: %d tool_use blocks, IDs: %v", requestId, toolUseCount, toolUseIDs)
		}
	}

	// Only send text block stop if we started one and haven't closed it
	if textBlockStarted {
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", contentIndex)
		flusher.Flush()
	}

	// Determine stop_reason based on content (fix: use dedicated tracking variables)
	stopReason := "end_turn"
	if hasToolUse {
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
									// Generate ID with embedded function name: {funcName}-{random8hex}
									id = mappers.GenerateToolUseIDFromHandler(name)
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

		// Get account email using common helper
		accountEmail := GetAccountEmail(r, tokenMgr)

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

// cleanSchemaForGemini recursively removes JSON Schema fields not supported by Gemini API
// and Claude API's JSON Schema draft 2020-12 requirements.
// This handles: $schema, exclusiveMinimum, exclusiveMaximum, and other unsupported fields.
func cleanSchemaForGemini(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}

	// Create a copy to avoid modifying the original
	cleaned := make(map[string]interface{})

	// Fields not supported by Gemini API and/or Claude API (JSON Schema draft 2020-12)
	// Claude rejects 'default', 'examples', 'format' in VALIDATED mode
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
		// Additional fields that Claude API rejects
		"default":    true,
		"examples":   true,
		"format":     true,
		"minLength":  true,
		"maxLength":  true,
		"minItems":   true,
		"maxItems":   true,
		"pattern":    true,
		"const":      true,
		"deprecated": true,
		"readOnly":   true,
		"writeOnly":  true,
		"$comment":   true,
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

// normalizeClaudeMessages splits user messages that mix tool_result with other content types.
// Anthropic API requires that when an assistant message contains tool_use, the following
// user message must contain ONLY tool_result blocks, not mixed with text or other content.
// This function splits such mixed messages into separate messages to comply with the API spec.
func normalizeClaudeMessages(messages []interface{}) []interface{} {
	var normalized []interface{}

	for _, msg := range messages {
		m, ok := msg.(map[string]interface{})
		if !ok {
			normalized = append(normalized, msg)
			continue
		}

		role, _ := m["role"].(string)
		if role != "user" {
			normalized = append(normalized, msg)
			continue
		}

		content, ok := m["content"].([]interface{})
		if !ok {
			normalized = append(normalized, msg)
			continue
		}

		// Separate tool_result blocks from other content types
		var toolResults, others []interface{}
		for _, block := range content {
			b, ok := block.(map[string]interface{})
			if !ok {
				others = append(others, block)
				continue
			}
			blockType, _ := b["type"].(string)
			if blockType == "tool_result" {
				toolResults = append(toolResults, block)
			} else {
				others = append(others, block)
			}
		}

		// If message contains both tool_result and other content, split into separate messages
		if len(toolResults) > 0 && len(others) > 0 {
			// First: message with only tool_result blocks
			normalized = append(normalized, map[string]interface{}{
				"role":    "user",
				"content": toolResults,
			})
			// Second: message with other content (text, images, etc.)
			normalized = append(normalized, map[string]interface{}{
				"role":    "user",
				"content": others,
			})
			log.Printf("🔧 Normalized user message: split %d tool_result + %d other blocks into 2 messages",
				len(toolResults), len(others))
		} else {
			// No split needed, keep original message
			normalized = append(normalized, msg)
		}
	}

	return normalized
}
