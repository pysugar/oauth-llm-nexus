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

// OpenAIChatHandler handles /v1/chat/completions
func OpenAIChatHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse request first to get model name
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			writeOpenAIError(w, "Failed to read request body", http.StatusBadRequest)
			return
		}

		// Generate requestId using common helper
		requestId := GetOrGenerateRequestID(r)

		// Verbose logging controlled by NEXUS_VERBOSE
		verbose := IsVerbose()
		if verbose {
			log.Printf("📥 [VERBOSE] [%s] /v1/chat/completions Raw request:\n%s", requestId, util.TruncateBytes(bodyBytes))
		}

		var req mappers.OpenAIChatRequest
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			log.Printf("⚠️ OpenAI parse error: %v", err)
			writeOpenAIError(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Resolve model mapping WITH provider
		targetModel, provider := db.ResolveModelWithProvider(req.Model)
		log.Printf("🗺️ OpenAI model routing: %s -> %s (provider: %s)", req.Model, targetModel, provider)

		// Route based on provider
		switch provider {
		case "codex":
			// Route to Codex handler - no Google token needed
			var chatReqMap map[string]interface{}
			json.Unmarshal(bodyBytes, &chatReqMap)
			chatReqMap["model"] = targetModel // Use resolved target model
			handleCodexChatRequest(w, chatReqMap, requestId)
			return

		default:
			// Google Cloud Code flow (existing behavior)
			cachedToken, err := GetTokenFromRequest(r, tokenMgr)
			if err != nil {
				writeOpenAIError(w, "No valid token available: "+err.Error(), http.StatusUnauthorized)
				return
			}

			// Convert to Gemini format
			geminiPayload := mappers.OpenAIToGemini(req, targetModel, cachedToken.ProjectID)

			// Convert to map and add missing Cloud Code API fields
			payloadBytes, _ := json.Marshal(geminiPayload)
			var payload map[string]interface{}
			json.Unmarshal(payloadBytes, &payload)

			// Add Cloud Code API required fields
			payload["userAgent"] = "antigravity"
			payload["requestType"] = "agent" // Restored per Antigravity-Manager reference
			payload["requestId"] = requestId

			// Verbose: Log Gemini payload before sending
			if verbose {
				geminiPayloadBytes, _ := json.MarshalIndent(payload, "", "  ")
				log.Printf("📤 [VERBOSE] [%s] /v1/chat/completions Gemini Request Payload:\n%s", requestId, util.TruncateBytes(geminiPayloadBytes))
			}

			if req.Stream {
				handleOpenAIStreaming(w, upstreamClient, cachedToken.AccessToken, payload, req.Model, requestId)
			} else {
				handleOpenAINonStreaming(w, upstreamClient, cachedToken.AccessToken, payload, req.Model, requestId)
			}
		}
	}
}

func handleOpenAINonStreaming(w http.ResponseWriter, client *upstream.Client, token string, payload map[string]interface{}, model string, requestId string) {
	verbose := IsVerbose()

	// Use SmartGenerateContent for automatic premium model handling
	resp, err := client.SmartGenerateContent(token, payload)
	if err != nil {
		if verbose {
			log.Printf("❌ [VERBOSE] [%s] /v1/chat/completions Upstream error: %v", requestId, err)
		}
		writeOpenAIError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil && verbose {
		log.Printf("⚠️ [VERBOSE] [%s] /v1/chat/completions ReadAll error: %v", requestId, err)
	}

	if resp.StatusCode != http.StatusOK {
		if verbose {
			log.Printf("❌ [VERBOSE] [%s] /v1/chat/completions Gemini API error (status %d):\n%s", requestId, resp.StatusCode, util.TruncateBytes(body))
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
		log.Printf("📥 [VERBOSE] /v1/chat/completions Gemini API Response:\n%s", util.TruncateBytes(prettyBytes))
	}

	var wrapped map[string]interface{}
	if err := json.Unmarshal(body, &wrapped); err != nil && verbose {
		log.Printf("⚠️ [VERBOSE] [%s] /v1/chat/completions Unmarshal error: %v", requestId, err)
	}

	geminiResp, ok := wrapped["response"].(map[string]interface{})
	if !ok {
		// Fallback: try using body directly
		json.Unmarshal(body, &geminiResp)
	}

	openaiResp, err := mappers.GeminiToOpenAI(geminiResp, model, false)
	if err != nil {
		if verbose {
			log.Printf("❌ [VERBOSE] [%s] /v1/chat/completions Conversion error: %v", requestId, err)
		}
		writeOpenAIError(w, "Response conversion error", http.StatusInternalServerError)
		return
	}

	// P1.2: Extract grounding metadata and convert to annotations
	groundingMetadata := mappers.ExtractGroundingMetadata(wrapped)
	if groundingMetadata != nil && len(groundingMetadata.GroundingChunks) > 0 {
		annotations := mappers.ConvertGroundingMetadataToAnnotations(groundingMetadata)
		if len(annotations) > 0 {
			// Inject annotations into the response
			var respMap map[string]interface{}
			json.Unmarshal(openaiResp, &respMap)

			if choices, ok := respMap["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if msg, ok := choice["message"].(map[string]interface{}); ok {
						msg["annotations"] = annotations
						if verbose {
							log.Printf("🔗 [VERBOSE] [%s] Added %d grounding annotations", requestId, len(annotations))
						}
					}
				}
			}

			openaiResp, _ = json.Marshal(respMap)
		}
	}

	// Verbose: Log final OpenAI response with empty content detection
	if verbose {
		var prettyResp map[string]interface{}
		json.Unmarshal(openaiResp, &prettyResp)
		prettyBytes, _ := json.MarshalIndent(prettyResp, "", "  ")
		log.Printf("📤 [VERBOSE] [%s] /v1/chat/completions Final Response:\n%s", requestId, util.TruncateBytes(prettyBytes))
		// Warn if response appears empty
		if len(openaiResp) < 100 {
			log.Printf("⚠️ [VERBOSE] [%s] Response is suspiciously short (%d bytes) - possible empty content", requestId, len(openaiResp))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(openaiResp)
}

func handleOpenAIStreaming(w http.ResponseWriter, client *upstream.Client, token string, payload map[string]interface{}, model string, requestId string) {
	// Use SmartStreamGenerateContent for automatic premium model handling
	resp, err := client.SmartStreamGenerateContent(token, payload)
	if err != nil {
		writeOpenAIError(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Check upstream status before switching to SSE (streaming reliability fix)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if IsVerbose() {
			log.Printf("❌ [VERBOSE] [%s] /v1/chat/completions Streaming upstream error (status %d):\n%s", requestId, resp.StatusCode, util.TruncateBytes(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
		return
	}

	SetSSEHeaders(w)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, "Streaming not supported", http.StatusInternalServerError)
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

			// Verbose: log raw streaming chunk (truncated for large chunks)
			if IsVerbose() {
				log.Printf("📦 [VERBOSE] [%s] /v1/chat/completions Stream chunk #%d: %s", requestId, chunkCount+1, util.TruncateLog(data, 512))
			}

			// Parse and unwrap response field
			var wrapped map[string]interface{}
			if err := json.Unmarshal([]byte(data), &wrapped); err != nil {
				if IsVerbose() {
					log.Printf("⚠️ [VERBOSE] [%s] /v1/chat/completions Stream parse error: %v", requestId, err)
				}
				continue
			}

			geminiResp, ok := wrapped["response"].(map[string]interface{})
			if !ok {
				json.Unmarshal([]byte(data), &geminiResp)
			}

			openaiChunk, err := mappers.GeminiToOpenAI(geminiResp, model, true)
			if err != nil {
				if IsVerbose() {
					log.Printf("⚠️ [VERBOSE] [%s] /v1/chat/completions Stream convert error: %v", requestId, err)
				}
				continue
			}

			if openaiChunk == nil {
				continue
			}

			// Verbose: log converted chunk (truncated)
			if IsVerbose() {
				log.Printf("📤 [VERBOSE] [%s] /v1/chat/completions Converted chunk: %s", requestId, util.TruncateLog(string(openaiChunk), 512))
			}

			fmt.Fprintf(w, "data: %s\n\n", openaiChunk)
			flusher.Flush()
			chunkCount++
		}
	}
	// Check scanner error after loop (streaming reliability fix)
	if err := scanner.Err(); err != nil && IsVerbose() {
		log.Printf("❌ [VERBOSE] [%s] /v1/chat/completions Scanner error: %v", requestId, err)
	}
	// Summary log for diagnosing empty responses
	if IsVerbose() {
		if chunkCount == 0 {
			log.Printf("⚠️ [VERBOSE] [%s] /v1/chat/completions Streaming completed with 0 chunks - client received empty response!", requestId)
		} else {
			log.Printf("✅ [VERBOSE] [%s] /v1/chat/completions Streaming completed: %d chunks sent", requestId, chunkCount)
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

// OpenAIChatHandlerWithMonitor wraps OpenAIChatHandler with request logging
func OpenAIChatHandlerWithMonitor(tokenMgr *token.Manager, upstreamClient *upstream.Client, pm *monitor.ProxyMonitor) http.HandlerFunc {
	baseHandler := OpenAIChatHandler(tokenMgr, upstreamClient)

	return func(w http.ResponseWriter, r *http.Request) {
		if !pm.IsEnabled() {
			baseHandler(w, r)
			return
		}

		startTime := time.Now()

		// Read and restore body for logging
		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

		// Extract model and stream flag from request
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		json.Unmarshal(bodyBytes, &req)

		// Get account email using common helper
		accountEmail := GetAccountEmail(r, tokenMgr)

		// For streaming requests, we can't wrap the response writer
		// Just call base handler and log basic info
		if req.Stream {
			baseHandler(w, r)

			pm.LogRequest(models.RequestLog{
				Method:       r.Method,
				URL:          r.URL.Path,
				Status:       200, // Assume success for streams
				Duration:     time.Since(startTime).Milliseconds(),
				Model:        req.Model,
				MappedModel:  db.ResolveModel(req.Model, "google"),
				AccountEmail: accountEmail,
				RequestBody:  string(bodyBytes),
				ResponseBody: "[streaming response]",
			})
			return
		}

		// Use response recorder to capture status and body (non-streaming only)
		rec := &responseRecorder{ResponseWriter: w, statusCode: 200}

		baseHandler(rec, r)

		// Extract tokens and error from response
		var inputTokens, outputTokens int
		var errorMsg string
		respBody := rec.body.String()

		if rec.statusCode >= 200 && rec.statusCode < 400 {
			// Parse usage from OpenAI response
			var resp struct {
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(respBody), &resp) == nil {
				inputTokens = resp.Usage.PromptTokens
				outputTokens = resp.Usage.CompletionTokens
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

// responseRecorder wraps http.ResponseWriter to capture status code and body
// Also implements http.Flusher for streaming support
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       strings.Builder
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body.Write(b) // Capture for logging
	return r.ResponseWriter.Write(b)
}

// Flush implements http.Flusher interface for streaming support
func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
