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
	"github.com/pysugar/oauth-llm-nexus/internal/upstream/geminikey"
)

// GeminiAIStudioProvider is a global provider used by Gemini API v1beta endpoints.
// It is initialized at startup when NEXUS_GEMINI_API_KEY or GEMINI_API_KEY is set.
var GeminiAIStudioProvider *geminikey.Provider

// InitGeminiAIStudioProviderFromEnv initializes the provider from environment variables.
// Returns true when the provider is enabled.
func InitGeminiAIStudioProviderFromEnv() bool {
	GeminiAIStudioProvider = geminikey.NewProviderFromEnv()
	return GeminiAIStudioProvider != nil && GeminiAIStudioProvider.IsEnabled()
}

// GeminiAIStudioProxyHandler handles Gemini API transparent proxy endpoints:
// - GET  /v1beta/models
// - GET  /v1beta/models/{model}
// - POST /v1beta/models/{model}:generateContent
// - POST /v1beta/models/{model}:streamGenerateContent
// - POST /v1beta/models/{model}:countTokens
// - POST /v1beta/models/{model}:embedContent
// - POST /v1beta/models/{model}:batchEmbedContents
func GeminiAIStudioProxyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if GeminiAIStudioProvider == nil || !GeminiAIStudioProvider.IsEnabled() {
			writeGeminiAIStudioError(w, "Gemini API proxy is not enabled", http.StatusServiceUnavailable)
			return
		}

		_, _, ok := parseGeminiAIStudioPath(r.Method, r.URL.Path)
		if !ok {
			writeGeminiAIStudioError(w, "Unsupported endpoint", http.StatusNotFound)
			return
		}

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			writeGeminiAIStudioError(w, "Failed to read request body", http.StatusBadRequest)
			return
		}

		resp, err := GeminiAIStudioProvider.Forward(
			r.Context(),
			r.Method,
			r.URL.Path,
			r.URL.Query(),
			r.Header,
			bodyBytes,
		)
		if err != nil {
			log.Printf("❌ Gemini API proxy error: method=%s path=%s err=%v", r.Method, r.URL.Path, err)
			writeGeminiAIStudioError(w, "Upstream proxy error: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		if err := geminikey.CopyResponse(w, resp); err != nil {
			log.Printf("❌ Gemini API response copy error: path=%s err=%v", r.URL.Path, err)
		}
	}
}

// GeminiAIStudioProxyHandlerWithMonitor wraps GeminiAIStudioProxyHandler with request logging.
func GeminiAIStudioProxyHandlerWithMonitor(pm *monitor.ProxyMonitor) http.HandlerFunc {
	baseHandler := GeminiAIStudioProxyHandler()

	return func(w http.ResponseWriter, r *http.Request) {
		if !pm.IsEnabled() {
			baseHandler(w, r)
			return
		}

		startTime := time.Now()
		model, action, _ := parseGeminiAIStudioPath(r.Method, r.URL.Path)
		if action == "" {
			action = strings.ToLower(r.Method)
		}

		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

		if action == "streamGenerateContent" {
			sw := &streamSnippetRecorder{ResponseWriter: w, statusCode: http.StatusOK}
			baseHandler(sw, r)
			pm.LogRequest(models.RequestLog{
				Method:       r.Method,
				URL:          r.URL.Path,
				Status:       sw.statusCode,
				Duration:     time.Since(startTime).Milliseconds(),
				Provider:     "gemini",
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
			Provider:     "gemini",
			Model:        model,
			MappedModel:  model,
			Error:        errorMsg,
			RequestBody:  string(bodyBytes),
			ResponseBody: respBody,
		})
	}
}

func parseGeminiAIStudioPath(method string, path string) (model string, action string, ok bool) {
	const prefix = "/v1beta/models"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}

	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		if method == http.MethodGet {
			return "", "models.list", true
		}
		return "", "", false
	}

	if !strings.HasPrefix(rest, "/") {
		return "", "", false
	}

	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		if method == http.MethodGet {
			return "", "models.list", true
		}
		return "", "", false
	}

	if method == http.MethodGet {
		if strings.Contains(rest, ":") {
			return "", "", false
		}
		return rest, "models.get", true
	}

	if method != http.MethodPost {
		return "", "", false
	}

	actions := []string{
		"generateContent",
		"streamGenerateContent",
		"countTokens",
		"embedContent",
		"batchEmbedContents",
	}
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

func writeGeminiAIStudioError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"code":    status,
		},
	})
}
