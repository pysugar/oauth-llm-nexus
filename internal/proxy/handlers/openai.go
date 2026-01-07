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
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/mappers"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
	"gorm.io/gorm"
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
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			writeOpenAIError(w, "Failed to read request body", http.StatusBadRequest)
			return
		}
		// Verbose logging controlled by NEXUS_VERBOSE
		verbose := IsVerbose()
		if verbose {
			log.Printf("📥 [VERBOSE] OpenAI raw request:\n%s", string(bodyBytes))
		}

		var req mappers.OpenAIChatRequest
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			log.Printf("⚠️ OpenAI parse error: %v", err)
			writeOpenAIError(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Resolve model mapping
		targetModel := db.ResolveModel(req.Model, "google")
		log.Printf("🗺️ OpenAI model routing: %s -> %s", req.Model, targetModel)

		// Convert to Gemini format
		// We pass the resolved target model to the mapper
		geminiPayload := mappers.OpenAIToGemini(req, targetModel, cachedToken.ProjectID)

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
	verbose := IsVerbose()

	resp, err := client.GenerateContent(token, payload)
	if err != nil {
		if verbose {
			log.Printf("❌ [VERBOSE] /v1/chat/completions Upstream error: %v", err)
		}
		writeOpenAIError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		if verbose {
			log.Printf("❌ [VERBOSE] /v1/chat/completions Gemini API error (status %d):\n%s", resp.StatusCode, string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
		return
	}

	// Verbose: Log raw Gemini response
	if verbose {
		var prettyBody map[string]interface{}
		json.Unmarshal(body, &prettyBody)
		prettyBytes, _ := json.MarshalIndent(prettyBody, "", "  ")
		log.Printf("📥 [VERBOSE] /v1/chat/completions Gemini API Response:\n%s", string(prettyBytes))
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
		if verbose {
			log.Printf("❌ [VERBOSE] /v1/chat/completions Conversion error: %v", err)
		}
		writeOpenAIError(w, "Response conversion error", http.StatusInternalServerError)
		return
	}

	// Verbose: Log final OpenAI response
	if verbose {
		var prettyResp map[string]interface{}
		json.Unmarshal(openaiResp, &prettyResp)
		prettyBytes, _ := json.MarshalIndent(prettyResp, "", "  ")
		log.Printf("📤 [VERBOSE] /v1/chat/completions Final Response:\n%s", string(prettyBytes))
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

	// Check upstream status before switching to SSE (streaming reliability fix)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if IsVerbose() {
			log.Printf("❌ [VERBOSE] /v1/chat/completions Streaming upstream error (status %d):\n%s", resp.StatusCode, string(body))
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
		writeOpenAIError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Increase scanner buffer to handle large SSE frames (1MB limit)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

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
	// Check scanner error after loop (streaming reliability fix)
	if err := scanner.Err(); err != nil && IsVerbose() {
		log.Printf("❌ [VERBOSE] /v1/chat/completions Scanner error: %v", err)
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
// Returns models declared in config that have active routes
func OpenAIModelsListHandler(database *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Get declared models from config
		declaredModels, err := db.GetConfigModels(database, "openai_models")
		if err != nil {
			log.Printf("⚠️ Failed to load openai_models from config: %v", err)
			// Fallback to empty list
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data":   []map[string]interface{}{},
			})
			return
		}

		// 2. Get set of client models that have active routes
		routedModels := db.GetClientModelsSet(database)

		// 3. Filter: only return models that are both declared AND routed
		var validModels []map[string]interface{}
		for _, model := range declaredModels {
			modelID, ok := model["id"].(string)
			if ok && routedModels[modelID] {
				validModels = append(validModels, model)
			}
		}

		// 4. Return OpenAI-compatible response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data":   validModels,
		})
	}
}
