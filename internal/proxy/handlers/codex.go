package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/upstream/codex"
)

// CodexProvider is a global Codex provider instance
// Initialized at startup, may be nil if auth.json is not available
var CodexProvider *codex.Provider

// InitCodexProvider initializes the global Codex provider
func InitCodexProvider(authPath string) error {
	CodexProvider = codex.NewProvider(authPath)
	return CodexProvider.Init()
}

// handleCodexChatRequest handles Chat Completions requests for Codex models
func handleCodexChatRequest(w http.ResponseWriter, chatReq map[string]interface{}, requestId string) {
	if CodexProvider == nil {
		writeOpenAIError(w, "Codex provider not initialized", http.StatusServiceUnavailable)
		return
	}

	// Convert Chat Completions to Responses format
	responsesReq := codex.ChatCompletionToResponses(chatReq)

	// Get client's stream preference
	clientStream, _ := chatReq["stream"].(bool)
	model, _ := chatReq["model"].(string)

	// Call Codex API (always streaming internally)
	resp, err := CodexProvider.StreamResponses(responsesReq)
	if err != nil {
		log.Printf("❌ [%s] Codex API error: %v", requestId, err)
		writeOpenAIError(w, "Codex API error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body := codex.ReadErrorBody(resp)
		log.Printf("❌ [%s] Codex API error (status %d): %s", requestId, resp.StatusCode, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write([]byte(body))
		return
	}

	if clientStream {
		// Client wants streaming: convert Codex SSE to OpenAI SSE
		streamCodexToOpenAI(w, resp.Body, model, requestId)
	} else {
		// Client wants non-streaming: collect and convert
		collectCodexToOpenAI(w, resp.Body, model, requestId)
	}
}

// streamCodexToOpenAI converts Codex SSE stream to OpenAI Chat Completions SSE
func streamCodexToOpenAI(w http.ResponseWriter, body io.Reader, model, requestId string) {
	SetSSEHeaders(w)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 10*1024*1024) // 10MB buffer

	chunkCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)

		switch eventType {
		case "response.output_text.delta":
			delta, _ := event["delta"].(string)
			itemId, _ := event["item_id"].(string)

			chunk := map[string]interface{}{
				"id":     itemId,
				"object": "chat.completion.chunk",
				"model":  model,
				"choices": []interface{}{
					map[string]interface{}{
						"index": 0,
						"delta": map[string]interface{}{
							"content": delta,
						},
					},
				},
			}
			chunkJSON, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", chunkJSON)
			flusher.Flush()
			chunkCount++

		case "response.completed":
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			log.Printf("✅ [%s] Codex streaming completed: %d chunks", requestId, chunkCount)
			return
		}
	}

	// If we get here without response.completed, still send DONE
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// collectCodexToOpenAI collects Codex SSE and converts to non-streaming response
func collectCodexToOpenAI(w http.ResponseWriter, body io.Reader, model, requestId string) {
	var fullText strings.Builder
	var usage map[string]interface{}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)

		switch eventType {
		case "response.output_text.delta":
			if delta, ok := event["delta"].(string); ok {
				fullText.WriteString(delta)
			}
		case "response.completed":
			if resp, ok := event["response"].(map[string]interface{}); ok {
				usage, _ = resp["usage"].(map[string]interface{})
			}
		}
	}

	response := map[string]interface{}{
		"id":     "chatcmpl-codex-" + requestId,
		"object": "chat.completion",
		"model":  model,
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": fullText.String(),
				},
				"finish_reason": "stop",
			},
		},
	}
	if usage != nil {
		response["usage"] = usage
	}

	log.Printf("✅ [%s] Codex non-streaming completed: %d chars", requestId, fullText.Len())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// CodexQuotaHandler handles /v1/codex/quota endpoint
func CodexQuotaHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if CodexProvider == nil {
			writeOpenAIError(w, "Codex provider not initialized", http.StatusServiceUnavailable)
			return
		}

		quota := CodexProvider.GetQuota()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"email":      quota.Email,
			"plan_type":  quota.PlanType,
			"account_id": quota.AccountID,
			"has_access": quota.HasAccess,
			"models":     codex.SupportedCodexModels(),
		})
	}
}

// handleCodexResponsesPassthrough handles /v1/responses requests for Codex models
// It normalizes the request format and passes through to Codex API
func handleCodexResponsesPassthrough(w http.ResponseWriter, bodyBytes []byte, targetModel, requestId string) {
	if CodexProvider == nil {
		writeOpenAIError(w, "Codex provider not initialized", http.StatusServiceUnavailable)
		return
	}

	// Parse request
	var payload map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		writeOpenAIError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Update model to target model
	payload["model"] = targetModel

	// Normalize input to Codex format: [{type:message, role, content:[{type:input_text, text}]}]
	normalizeCodexInput(payload)

	// Set required Codex fields (per CLIProxyAPI reference)
	payload["stream"] = true
	payload["store"] = false
	payload["parallel_tool_calls"] = true
	delete(payload, "max_output_tokens")
	delete(payload, "max_completion_tokens")
	delete(payload, "temperature")
	delete(payload, "top_p")
	delete(payload, "service_tier")

	// Call Codex API
	resp, err := CodexProvider.StreamResponses(payload)
	if err != nil {
		log.Printf("❌ [%s] Codex Responses API error: %v", requestId, err)
		writeOpenAIError(w, "Codex API error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Handle non-200 responses
	if resp.StatusCode != http.StatusOK {
		body := codex.ReadErrorBody(resp)
		log.Printf("❌ [%s] Codex API error (status %d): %s", requestId, resp.StatusCode, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write([]byte(body))
		return
	}

	// Stream response directly to client (passthrough)
	SetSSEHeaders(w)
	log.Printf("🔄 [%s] Codex /responses passthrough streaming...", requestId)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Direct passthrough of SSE stream
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(nil, 10*1024*1024) // 10MB buffer
	eventCount := 0

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			// Empty line - flush SSE event
			fmt.Fprintf(w, "\n")
			flusher.Flush()
		} else {
			fmt.Fprintf(w, "%s\n", line)
			if strings.HasPrefix(line, "data: ") {
				eventCount++
			}
		}
	}

	log.Printf("✅ [%s] Codex /responses passthrough completed: %d events", requestId, eventCount)
}

// normalizeCodexInput normalizes the input field to Codex's required format:
// [{type: "message", role: "user", content: [{type: "input_text", text: "..."}]}]
func normalizeCodexInput(payload map[string]interface{}) {
	input, ok := payload["input"]
	if !ok {
		return
	}

	var normalizedInput []interface{}

	switch v := input.(type) {
	case string:
		// Simple string -> wrap in message format
		normalizedInput = []interface{}{
			map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": v},
				},
			},
		}
	case []interface{}:
		// Check if array elements are already in correct format
		for _, item := range v {
			if msg, ok := item.(map[string]interface{}); ok {
				// Check if it's a proper message object
				if _, hasType := msg["type"]; hasType {
					// Already has type field, convert system -> developer
					if role, _ := msg["role"].(string); role == "system" {
						msg["role"] = "developer"
					}
					normalizedInput = append(normalizedInput, msg)
				} else if role, hasRole := msg["role"]; hasRole {
					// Has role but no type, add type field
					msg["type"] = "message"
					// Normalize content if it's a string
					if content, ok := msg["content"].(string); ok {
						msg["content"] = []interface{}{
							map[string]interface{}{"type": "input_text", "text": content},
						}
					}
					if role == "system" {
						msg["role"] = "developer"
					}
					normalizedInput = append(normalizedInput, msg)
				}
			} else if text, ok := item.(string); ok {
				// Simple string in array -> wrap as user message
				normalizedInput = append(normalizedInput, map[string]interface{}{
					"type": "message",
					"role": "user",
					"content": []interface{}{
						map[string]interface{}{"type": "input_text", "text": text},
					},
				})
			}
		}
	}

	if len(normalizedInput) > 0 {
		payload["input"] = normalizedInput
	}
}
