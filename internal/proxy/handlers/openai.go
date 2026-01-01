package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/mappers"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
)

// OpenAIChatHandler handles /v1/chat/completions
func OpenAIChatHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get token: Check for explicit account header, else use Primary/Default
		var cachedToken *token.CachedToken
		var err error

		if accountHeader := r.Header.Get("X-Nexus-Account"); accountHeader != "" {
			// Explicit account selection
			cachedToken, err = tokenMgr.GetTokenByIdentifier(accountHeader)
			if err != nil {
				writeOpenAIError(w, fmt.Sprintf("Account not found: %s", accountHeader), http.StatusUnauthorized)
				return
			}
		} else {
			// Implicit: Use Primary or Default account
			cachedToken, err = tokenMgr.GetPrimaryOrDefaultToken()
			if err != nil {
				writeOpenAIError(w, "No valid token available", http.StatusUnauthorized)
				return
			}
		}

		// Parse request
		var req mappers.OpenAIChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOpenAIError(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		log.Printf("📨 OpenAI request: model=%s messages=%d stream=%v", req.Model, len(req.Messages), req.Stream)

		// Convert to Gemini format (already wrapped with project/requestId/model/request)
		geminiPayload := mappers.OpenAIToGemini(req, cachedToken.ProjectID)

		// Convert to map and add missing Cloud Code API fields
		payloadBytes, _ := json.Marshal(geminiPayload)
		var payload map[string]interface{}
		json.Unmarshal(payloadBytes, &payload)

		// Add Cloud Code API required fields
		payload["userAgent"] = "antigravity"
		payload["requestType"] = "gemini"
		payload["requestId"] = "agent-" + uuid.New().String() // Override with proper format

		if req.Stream {
			handleOpenAIStreaming(w, upstreamClient, cachedToken.AccessToken, payload, req.Model)
		} else {
			handleOpenAINonStreaming(w, upstreamClient, cachedToken.AccessToken, payload, req.Model)
		}
	}
}

func handleOpenAINonStreaming(w http.ResponseWriter, client *upstream.Client, token string, payload map[string]interface{}, model string) {
	resp, err := client.GenerateContent(token, payload)
	if err != nil {
		writeOpenAIError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
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
		// Fallback: try using body directly
		json.Unmarshal(body, &geminiResp)
	}

	openaiResp, err := mappers.GeminiToOpenAI(geminiResp, model, false)
	if err != nil {
		writeOpenAIError(w, "Response conversion error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(openaiResp)
}

func handleOpenAIStreaming(w http.ResponseWriter, client *upstream.Client, token string, payload map[string]interface{}, model string) {
	resp, err := client.StreamGenerateContent(token, payload)
	if err != nil {
		writeOpenAIError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
				break
			}

			// Parse and unwrap response field
			var wrapped map[string]interface{}
			if err := json.Unmarshal([]byte(data), &wrapped); err != nil {
				continue
			}

			geminiResp, ok := wrapped["response"].(map[string]interface{})
			if !ok {
				json.Unmarshal([]byte(data), &geminiResp)
			}

			openaiChunk, err := mappers.GeminiToOpenAI(geminiResp, model, true)
			if err != nil {
				continue
			}

			fmt.Fprintf(w, "data: %s\n\n", openaiChunk)
			flusher.Flush()
		}
	}
}

func writeOpenAIError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    "api_error",
			"code":    status,
		},
	})
}

// OpenAIModelsListHandler handles /v1/models
func OpenAIModelsListHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Return OpenAI-compatible model list
		models := []map[string]interface{}{
			{"id": "gpt-4", "object": "model", "owned_by": "nexus"},
			{"id": "gpt-4-turbo", "object": "model", "owned_by": "nexus"},
			{"id": "gpt-4o", "object": "model", "owned_by": "nexus"},
			{"id": "gpt-4o-mini", "object": "model", "owned_by": "nexus"},
			{"id": "gpt-3.5-turbo", "object": "model", "owned_by": "nexus"},
			{"id": "o1", "object": "model", "owned_by": "nexus"},
			{"id": "o1-mini", "object": "model", "owned_by": "nexus"},
			{"id": "gemini-2.5-pro", "object": "model", "owned_by": "nexus"},
			{"id": "gemini-2.5-flash", "object": "model", "owned_by": "nexus"},
			{"id": "gemini-3-pro", "object": "model", "owned_by": "nexus"},
			{"id": "gemini-3-flash", "object": "model", "owned_by": "nexus"},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data":   models,
		})
	}
}
