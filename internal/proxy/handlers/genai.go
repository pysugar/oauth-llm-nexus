package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
)

// GenAIHandler handles /genai/v1beta/models/{model}:generateContent
func GenAIHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawModel := chi.URLParam(r, "model")
		// Resolve model mapping (e.g. gemini-2.0-flash -> gemini-3-flash)
		model := db.ResolveModel(rawModel, "google")

		// Get token: Check for explicit account header, else use Primary/Default
		var cachedToken *token.CachedToken
		var err error

		if accountHeader := r.Header.Get("X-Nexus-Account"); accountHeader != "" {
			cachedToken, err = tokenMgr.GetTokenByIdentifier(accountHeader)
			if err != nil {
				writeGenAIError(w, "Account not found: "+accountHeader, http.StatusUnauthorized)
				return
			}
		} else {
			cachedToken, err = tokenMgr.GetPrimaryOrDefaultToken()
			if err != nil {
				writeGenAIError(w, "No valid token", http.StatusUnauthorized)
				return
			}
		}

		// Parse request body
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			writeGenAIError(w, "Invalid request", http.StatusBadRequest)
			return
		}

		log.Printf("📨 GenAI request: model=%s", model)

		// Generate requestId early so all logs can use it
		// Use client-provided X-Request-ID if present, otherwise generate new one
		requestId := r.Header.Get("X-Request-ID")
		if requestId == "" {
			requestId = "agent-" + uuid.New().String()
		}
		if IsVerbose() {
			reqBytes, _ := json.MarshalIndent(reqBody, "", "  ")
			log.Printf("📥 [VERBOSE] [%s] /genai/v1beta Raw request:\n%s", requestId, string(reqBytes))
		}

		// Build Cloud Code API payload (wrapped format)
		payload := map[string]interface{}{
			"project":     cachedToken.ProjectID,
			"requestId":   requestId,
			"request":     reqBody,
			"model":       model,
			"userAgent":   "antigravity",
			"requestType": "gemini",
		}

		// Verbose: Log Gemini payload before sending
		if IsVerbose() {
			geminiPayloadBytes, _ := json.MarshalIndent(payload, "", "  ")
			log.Printf("📤 [VERBOSE] [%s] /genai/v1beta Gemini Request Payload:\n%s", requestId, string(geminiPayloadBytes))
		}

		resp, err := upstreamClient.GenerateContent(cachedToken.AccessToken, payload)
		if err != nil {
			if IsVerbose() {
				log.Printf("❌ [VERBOSE] [%s] /genai/v1beta Upstream error: %v", requestId, err)
			}
			writeGenAIError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusOK {
			if IsVerbose() {
				var prettyErr map[string]interface{}
				json.Unmarshal(body, &prettyErr)
				prettyBytes, _ := json.MarshalIndent(prettyErr, "", "  ")
				log.Printf("❌ [VERBOSE] /genai/v1beta Gemini API error (status %d):\n%s", resp.StatusCode, string(prettyBytes))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write(body)
			return
		}

		// Unwrap response: Cloud Code API returns {"response": {...}}
		var wrapped map[string]interface{}
		if err := json.Unmarshal(body, &wrapped); err != nil {
			if IsVerbose() {
				log.Printf("❌ [VERBOSE] /genai/v1beta Failed to parse response: %v\nRaw: %s", err, string(body))
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
			return
		}

		// Stage 3: Verbose logging for Gemini response
		if IsVerbose() {
			prettyBytes, _ := json.MarshalIndent(wrapped, "", "  ")
			log.Printf("📥 [VERBOSE] Gemini API Response:\n%s", string(prettyBytes))
		}

		if inner, ok := wrapped["response"]; ok {
			// Stage 4: Verbose logging for final GenAI response
			if IsVerbose() {
				innerBytes, _ := json.MarshalIndent(inner, "", "  ")
				log.Printf("📤 [VERBOSE] /genai/v1beta Final Response:\n%s", string(innerBytes))
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(inner)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
		}
	}
}

// GenAIStreamHandler handles /genai/v1beta/models/{model}:streamGenerateContent
func GenAIStreamHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawModel := chi.URLParam(r, "model")
		// Resolve model mapping
		model := db.ResolveModel(rawModel, "google")

		// Get token: Check for explicit account header, else use Primary/Default
		var cachedToken *token.CachedToken
		var err error

		if accountHeader := r.Header.Get("X-Nexus-Account"); accountHeader != "" {
			cachedToken, err = tokenMgr.GetTokenByIdentifier(accountHeader)
			if err != nil {
				writeGenAIError(w, "Account not found: "+accountHeader, http.StatusUnauthorized)
				return
			}
		} else {
			cachedToken, err = tokenMgr.GetPrimaryOrDefaultToken()
			if err != nil {
				writeGenAIError(w, "No valid token", http.StatusUnauthorized)
				return
			}
		}

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			writeGenAIError(w, "Invalid request", http.StatusBadRequest)
			return
		}

		log.Printf("📨 GenAI stream request: model=%s", model)

		// Stage 1: Verbose logging for raw GenAI stream request
		// Use client-provided X-Request-ID if present, otherwise generate new one
		requestId := r.Header.Get("X-Request-ID")
		if requestId == "" {
			requestId = "agent-" + uuid.New().String()
		}
		if IsVerbose() {
			reqBytes, _ := json.MarshalIndent(reqBody, "", "  ")
			log.Printf("📥 [VERBOSE] [%s] GenAI stream raw request:\n%s", requestId, string(reqBytes))
		}

		payload := map[string]interface{}{
			"project":     cachedToken.ProjectID,
			"requestId":   requestId,
			"request":     reqBody,
			"model":       model,
			"userAgent":   "antigravity",
			"requestType": "gemini",
		}

		// Verbose: Log Gemini payload before sending
		if IsVerbose() {
			geminiPayloadBytes, _ := json.MarshalIndent(payload, "", "  ")
			log.Printf("📤 [VERBOSE] [%s] /genai/v1beta Gemini Stream Request Payload:\n%s", requestId, string(geminiPayloadBytes))
		}

		resp, err := upstreamClient.StreamGenerateContent(cachedToken.AccessToken, payload)
		if err != nil {
			writeGenAIError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// Check upstream status before switching to SSE (streaming reliability fix)
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			if IsVerbose() {
				log.Printf("❌ [VERBOSE] [%s] /genai/v1beta Streaming upstream error (status %d):\n%s", requestId, resp.StatusCode, string(body))
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
			writeGenAIError(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}

		// Increase scanner buffer to handle large SSE frames (8MB limit)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

		chunkCount := 0
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					fmt.Fprintf(w, "data: [DONE]\n\n")
					flusher.Flush()
					break
				}

				// Verbose: log raw streaming chunk
				if IsVerbose() {
					log.Printf("📦 [VERBOSE] [%s] /genai/v1beta Stream chunk #%d: %s", requestId, chunkCount+1, data)
				}

				// Parse and unwrap response field
				var wrapped map[string]interface{}
				if err := json.Unmarshal([]byte(data), &wrapped); err != nil {
					if IsVerbose() {
						log.Printf("⚠️ [VERBOSE] [%s] /genai/v1beta Stream parse error: %v", requestId, err)
					}
					// Pass through if can't parse
					fmt.Fprintf(w, "data: %s\n\n", data)
					flusher.Flush()
					continue
				}

				if inner, ok := wrapped["response"]; ok {
					innerBytes, _ := json.Marshal(inner)
					fmt.Fprintf(w, "data: %s\n\n", string(innerBytes))
				} else {
					fmt.Fprintf(w, "data: %s\n\n", data)
				}
				flusher.Flush()
				chunkCount++
			}
		}
		// Check scanner error after loop (streaming reliability fix)
		if err := scanner.Err(); err != nil && IsVerbose() {
			log.Printf("❌ [VERBOSE] [%s] /genai/v1beta Scanner error: %v", requestId, err)
		}
		// Summary log for diagnosing empty responses
		if IsVerbose() {
			if chunkCount == 0 {
				log.Printf("⚠️ [VERBOSE] [%s] /genai/v1beta Streaming completed with 0 chunks - client received empty response!", requestId)
			} else {
				log.Printf("✅ [VERBOSE] [%s] /genai/v1beta Streaming completed: %d chunks sent", requestId, chunkCount)
			}
		}
	}
}

func writeGenAIError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    status,
			"message": message,
			"status":  "ERROR",
		},
	})
}

// GenAIModelsListHandler handles /genai/v1beta/models (GET)
// GenAIModelsListHandler handles /genai/v1beta/models (GET)
func GenAIModelsListHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get valid token to call upstream
		cachedToken, err := tokenMgr.GetPrimaryOrDefaultToken()
		if err != nil {
			writeGenAIError(w, "No valid token available", http.StatusUnauthorized)
			return
		}

		// Fetch real models from upstream Google API
		// We use the internal 'fetchAvailableModels' endpoint exposed by Cloud Code
		resp, err := upstreamClient.FetchAvailableModels(cachedToken.AccessToken)
		if err != nil {
			log.Printf("⚠️ Failed to fetch upstream models: %v", err)
			// Fallback to static list if upstream fails
			writeStaticGenAIModels(w)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Printf("⚠️ Upstream models fetch returned status: %d", resp.StatusCode)
			writeStaticGenAIModels(w)
			return
		}

		// Pass through upstream response directly
		// It returns {"models": [...]} which matches standard GenAI format

		// Parse Cloud Code upstream response
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("⚠️ Failed to read upstream body: %v", err)
			writeStaticGenAIModels(w)
			return
		}

		var cloudCodeResp struct {
			Models map[string]struct {
				DisplayName string `json:"displayName"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &cloudCodeResp); err != nil {
			log.Printf("⚠️ Failed to parse Cloud Code models: %v", err)
			writeStaticGenAIModels(w)
			return
		}

		// Transform to standard GenAI format (models array)
		type GenAIModel struct {
			Name        string `json:"name"`
			Version     string `json:"version"`
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
		}

		var genAIModels []GenAIModel
		for id, details := range cloudCodeResp.Models {
			genAIModels = append(genAIModels, GenAIModel{
				Name:        "models/" + id,
				Version:     id,
				DisplayName: details.DisplayName,
				Description: details.DisplayName, // Use display name as description
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"models": genAIModels,
		})
	}
}

func writeStaticGenAIModels(w http.ResponseWriter) {
	models := []map[string]interface{}{
		{
			"name":        "models/gemini-2.0-flash-exp",
			"version":     "2.0-flash",
			"displayName": "Gemini 2.0 Flash",
			"description": "Fast and versatile multimodal model",
		},
		{
			"name":        "models/gemini-1.5-pro",
			"version":     "1.5-pro",
			"displayName": "Gemini 1.5 Pro",
			"description": "Mid-size multimodal model",
		},
		{
			"name":        "models/gemini-1.5-flash",
			"version":     "1.5-flash",
			"displayName": "Gemini 1.5 Flash",
			"description": "Fast and versatile multimodal model",
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"models": models,
	})
}
