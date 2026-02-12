package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream/vertexkey"
)

// VertexAIStudioProvider is a global provider used by Vertex AI proxy endpoints.
// It is initialized at startup only when NEXUS_VERTEX_API_KEY is set.
var VertexAIStudioProvider *vertexkey.Provider

// InitVertexAIStudioProviderFromEnv initializes the provider from environment variables.
// Returns true when the provider is enabled.
func InitVertexAIStudioProviderFromEnv() bool {
	VertexAIStudioProvider = vertexkey.NewProviderFromEnv()
	return VertexAIStudioProvider != nil && VertexAIStudioProvider.IsEnabled()
}

// VertexAIStudioProxyHandler handles:
// - POST /v1/publishers/google/models/{model}:generateContent
// - POST /v1/publishers/google/models/{model}:streamGenerateContent
// - POST /v1/publishers/google/models/{model}:countTokens
func VertexAIStudioProxyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if VertexAIStudioProvider == nil || !VertexAIStudioProvider.IsEnabled() {
			writeVertexAIStudioError(w, "Vertex AI proxy is not enabled", http.StatusServiceUnavailable)
			return
		}

		model, action, ok := parseVertexAIStudioModelAction(r.URL.Path)
		if !ok {
			writeVertexAIStudioError(w, "Unsupported endpoint", http.StatusNotFound)
			return
		}

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			writeVertexAIStudioError(w, "Failed to read request body", http.StatusBadRequest)
			return
		}

		resp, err := VertexAIStudioProvider.Forward(
			r.Context(),
			r.Method,
			model,
			action,
			r.URL.Query(),
			r.Header,
			bodyBytes,
		)
		if err != nil {
			log.Printf("❌ Vertex AI proxy error: model=%s action=%s err=%v", model, action, err)
			writeVertexAIStudioError(w, "Upstream proxy error: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		if err := vertexkey.CopyResponse(w, resp); err != nil {
			log.Printf("❌ Vertex AI response copy error: model=%s action=%s err=%v", model, action, err)
		}
	}
}

// VertexAIStudioProxyHandlerWithMonitor wraps VertexAIStudioProxyHandler with request logging.
func VertexAIStudioProxyHandlerWithMonitor(pm *monitor.ProxyMonitor) http.HandlerFunc {
	baseHandler := VertexAIStudioProxyHandler()

	return func(w http.ResponseWriter, r *http.Request) {
		if !pm.IsEnabled() {
			baseHandler(w, r)
			return
		}

		startTime := time.Now()
		model, action, _ := parseVertexAIStudioModelAction(r.URL.Path)

		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

		// Stream actions should avoid buffering full response body in memory.
		if action == "streamGenerateContent" {
			sw := &streamSnippetRecorder{ResponseWriter: w, statusCode: http.StatusOK}
			baseHandler(sw, r)
			pm.LogRequest(models.RequestLog{
				Method:       r.Method,
				URL:          r.URL.Path,
				Status:       sw.statusCode,
				Duration:     time.Since(startTime).Milliseconds(),
				Provider:     "vertex",
				Model:        model,
				MappedModel:  model,
				Error:        streamStatusError(sw.statusCode),
				RequestBody:  string(bodyBytes),
				ResponseBody: sw.Snippet(),
			})
			return
		}

		rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		baseHandler(rec, r)

		respBody := rec.body.String()
		var errorMsg string
		if rec.statusCode >= http.StatusBadRequest {
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

		pm.LogRequest(models.RequestLog{
			Method:       r.Method,
			URL:          r.URL.Path,
			Status:       rec.statusCode,
			Duration:     time.Since(startTime).Milliseconds(),
			Provider:     "vertex",
			Model:        model,
			MappedModel:  model,
			Error:        errorMsg,
			RequestBody:  string(bodyBytes),
			ResponseBody: respBody,
		})
	}
}

func parseVertexAIStudioModelAction(path string) (model string, action string, ok bool) {
	const prefix = "/v1/publishers/google/models/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}

	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return "", "", false
	}

	actions := []string{"generateContent", "streamGenerateContent", "countTokens"}
	for _, candidate := range actions {
		suffix := ":" + candidate
		if strings.HasSuffix(rest, suffix) {
			model = strings.TrimSuffix(rest, suffix)
			if strings.TrimSpace(model) == "" {
				return "", "", false
			}
			return model, candidate, true
		}
	}
	return "", "", false
}

func writeVertexAIStudioError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"code":    status,
		},
	})
}
