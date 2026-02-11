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
		writeOpenAIUpstreamError(w, resp.StatusCode, body)
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
		writeOpenAIUpstreamError(w, resp.StatusCode, body)
		return
	}

	SetSSEHeaders(w)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	chunkCount, _, scannerErr := streamOpenAIChatChunks(w, flusher, resp.Body, model, requestId)
	// Check scanner error after loop (streaming reliability fix)
	if scannerErr != nil {
		log.Printf("⚠️ [%s] /v1/chat/completions Scanner error: %v", requestId, scannerErr)
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

func streamOpenAIChatChunks(w io.Writer, flusher http.Flusher, stream io.Reader, model string, requestId string) (chunkCount int, doneSent bool, scannerErr error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	safetyChecker := NewStreamSafetyChecker()

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			doneSent = true
			break
		}
		if abort, reason := safetyChecker.CheckChunk([]byte(data)); abort {
			log.Printf("⚠️ [%s] /v1/chat/completions Stream aborted by safety checker: %s", requestId, reason)
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			doneSent = true
			return chunkCount, doneSent, nil
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

	scannerErr = scanner.Err()
	if scannerErr == nil && !doneSent {
		// Upstream may terminate the stream by EOF without sending [DONE].
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		doneSent = true
	}
	return chunkCount, doneSent, scannerErr
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

func writeOpenAIUpstreamError(w http.ResponseWriter, status int, upstreamBody []byte) {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buildOpenAIUpstreamErrorBody(status, upstreamBody))
}

func buildOpenAIUpstreamErrorBody(status int, upstreamBody []byte) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}

	message := http.StatusText(status)
	if strings.TrimSpace(message) == "" {
		message = "Upstream request failed"
	}
	errorCode := interface{}(status)

	var root map[string]interface{}
	var rootErr map[string]interface{}
	if json.Unmarshal(upstreamBody, &root) == nil {
		if nested, ok := root["error"].(map[string]interface{}); ok {
			rootErr = nested
		}

		if msg := firstNonEmptyString(
			valueFromMap(rootErr, "message"),
			valueFromMap(root, "message"),
		); msg != "" {
			message = msg
		}

		if code, ok := firstErrorCode(
			valueFromMap(rootErr, "code"),
			valueFromMap(root, "code"),
			valueFromMap(rootErr, "status"),
			valueFromMap(root, "status"),
		); ok {
			errorCode = code
		}
	} else if trimmed := strings.TrimSpace(string(upstreamBody)); trimmed != "" {
		message = trimmed
	}

	payload := map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    openAIErrorTypeFromStatus(status),
			"code":    errorCode,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"server_error","code":"internal_server_error"}}`, message))
	}
	return body
}

func openAIErrorTypeFromStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		if status >= http.StatusInternalServerError {
			return "server_error"
		}
		return "invalid_request_error"
	}
}

func valueFromMap(data map[string]interface{}, key string) interface{} {
	if data == nil {
		return nil
	}
	return data[key]
}

func firstNonEmptyString(values ...interface{}) string {
	for _, value := range values {
		str, ok := value.(string)
		if !ok {
			continue
		}
		str = strings.TrimSpace(str)
		if str != "" {
			return str
		}
	}
	return ""
}

func firstErrorCode(values ...interface{}) (interface{}, bool) {
	for _, value := range values {
		switch v := value.(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(v) != "" {
				return v, true
			}
		case float64:
			return int(v), true
		case float32:
			return int(v), true
		case int:
			return v, true
		case int8:
			return int(v), true
		case int16:
			return int(v), true
		case int32:
			return int(v), true
		case int64:
			return int(v), true
		case uint:
			return int(v), true
		case uint8:
			return int(v), true
		case uint16:
			return int(v), true
		case uint32:
			return int(v), true
		case uint64:
			return int(v), true
		}
	}
	return nil, false
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
		targetModel, provider := db.ResolveModelWithProvider(req.Model)

		// Get account email using common helper
		accountEmail := GetAccountEmail(r, tokenMgr)

		// For streaming requests, we can't wrap the response writer
		// Capture actual HTTP status without buffering streaming body.
		if req.Stream {
			sw := &streamSnippetRecorder{ResponseWriter: w, statusCode: http.StatusOK}
			baseHandler(sw, r)

			pm.LogRequest(models.RequestLog{
				Method:       r.Method,
				URL:          r.URL.Path,
				Status:       sw.statusCode,
				Duration:     time.Since(startTime).Milliseconds(),
				Provider:     provider,
				Model:        req.Model,
				MappedModel:  targetModel,
				AccountEmail: accountEmail,
				Error:        streamStatusError(sw.statusCode),
				RequestBody:  string(bodyBytes),
				ResponseBody: sw.Snippet(),
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
			Provider:     provider,
			Model:        req.Model,
			MappedModel:  targetModel,
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

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.statusCode = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.statusCode == 0 {
		s.statusCode = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func streamStatusError(statusCode int) string {
	if statusCode >= 200 && statusCode < 400 {
		return ""
	}
	return http.StatusText(statusCode)
}

const (
	// MaxStreamSnippetSize limits how many bytes of streaming response we capture for logging.
	// Large enough for useful diagnostics, small enough to avoid memory pressure.
	MaxStreamSnippetSize = 4096
)

// streamSnippetRecorder wraps http.ResponseWriter to capture the status code
// AND the first MaxStreamSnippetSize bytes of streaming output for logging.
// Once the snippet buffer is full, subsequent writes pass through with zero overhead.
type streamSnippetRecorder struct {
	http.ResponseWriter
	statusCode  int
	snippet     strings.Builder
	snippetDone bool // true once we've captured enough bytes
}

func (s *streamSnippetRecorder) WriteHeader(code int) {
	s.statusCode = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *streamSnippetRecorder) Write(b []byte) (int, error) {
	if !s.snippetDone {
		remaining := MaxStreamSnippetSize - s.snippet.Len()
		if remaining > 0 {
			if len(b) <= remaining {
				s.snippet.Write(b)
			} else {
				s.snippet.Write(b[:remaining])
				s.snippetDone = true
			}
		} else {
			s.snippetDone = true
		}
	}
	return s.ResponseWriter.Write(b)
}

func (s *streamSnippetRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Snippet returns the captured stream snippet for logging.
func (s *streamSnippetRecorder) Snippet() string {
	text := s.snippet.String()
	if s.snippetDone {
		return text + "...[truncated]"
	}
	return text
}
