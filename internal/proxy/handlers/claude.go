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
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/mappers"
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
							name, _ := b["name"].(string)
							input := b["input"]
							parts = append(parts, map[string]interface{}{
								"functionCall": map[string]interface{}{
									"name": name,
									"args": input,
								},
							})
						case "tool_result":
							// Convert to Gemini function response
							toolUseId, _ := b["tool_use_id"].(string)
							resultContent := b["content"]
							parts = append(parts, map[string]interface{}{
								"functionResponse": map[string]interface{}{
									"name":     toolUseId,
									"response": resultContent,
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

		payload := map[string]interface{}{
			"project":     cachedToken.ProjectID,
			"requestId":   requestId,
			"model":       model,
			"userAgent":   "antigravity",
			"requestType": "agent", // Restored per Antigravity-Manager reference
			"request": map[string]interface{}{
				"contents":  geminiContents,
				"sessionId": sessionId, // Required by oh-my-opencode for multi-turn conversations
				"generationConfig": map[string]interface{}{
					"maxOutputTokens": 64000,
				},
			},
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
	resp, err := client.GenerateContent(token, payload)
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
	resp, err := client.StreamGenerateContent(token, payload)
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

	// Send content_block_start
	fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	flusher.Flush()

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
				delta := &mappers.ClaudeDelta{Type: "text_delta", Text: text}
				deltaEvent, _ := mappers.CreateClaudeStreamEvent("content_block_delta", delta)
				fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", deltaEvent)
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

	// Send stop events
	fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	flusher.Flush()

	stopEvent, _ := mappers.CreateClaudeStreamEvent("message_delta", nil)
	fmt.Fprintf(w, "event: message_delta\ndata: %s\n\n", stopEvent)
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
