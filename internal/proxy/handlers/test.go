package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/providers/catalog"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
	"gorm.io/gorm"
)

const (
	testEndpointOpenAIChat       = "openai_chat"
	testEndpointOpenAIResponses  = "openai_responses"
	testEndpointAnthropic        = "anthropic_messages"
	testEndpointGenAIGenerate    = "genai_generate"
	testEndpointVertexGenerate   = "vertex_generate"
	testEndpointAIStudioGenerate = "gemini_api_generate"
	testEndpointOpenRouterChat   = "openrouter_generate"
	testEndpointNVIDIAChat       = "nvidia_generate"
)

// EndpointTestResult is the normalized response for /api/test.
type EndpointTestResult struct {
	Endpoint    string `json:"endpoint"`
	Path        string `json:"path"`
	Model       string `json:"model"`
	Provider    string `json:"provider,omitempty"`
	MappedModel string `json:"mapped_model,omitempty"`
	StatusCode  int    `json:"status_code"`
	DurationMS  int64  `json:"duration_ms"`
	ContentType string `json:"content_type"`
	Success     bool   `json:"success"`
	Skipped     bool   `json:"skipped,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
}

var runEndpointTest = executeEndpointTest

// TestHandler runs one endpoint-specific probe test without requiring client API keys.
func TestHandler(database *gorm.DB, tokenMgr *token.Manager, upstreamClient *upstream.Client, pm *monitor.ProxyMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		endpoint := strings.TrimSpace(r.URL.Query().Get("endpoint"))
		if endpoint == "" {
			writeOpenAIError(w, "Missing endpoint query parameter", http.StatusBadRequest)
			return
		}

		result, err := runEndpointTest(endpoint, database, tokenMgr, upstreamClient)
		if err != nil {
			writeOpenAIError(w, err.Error(), http.StatusBadRequest)
			return
		}

		if pm != nil && pm.IsEnabled() {
			var logError string
			if result.Skipped {
				logError = result.Reason
			} else if !result.Success {
				logError = result.Summary
			}

			pm.LogRequest(models.RequestLog{
				Method:       "POST",
				URL:          result.Path,
				Status:       result.StatusCode,
				Duration:     result.DurationMS,
				Provider:     result.Provider,
				Model:        result.Model,
				MappedModel:  result.MappedModel,
				Error:        logError,
				RequestBody:  fmt.Sprintf(`{"endpoint":"%s"}`, endpoint),
				ResponseBody: result.Snippet,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

func executeEndpointTest(endpoint string, database *gorm.DB, tokenMgr *token.Manager, upstreamClient *upstream.Client) (EndpointTestResult, error) {
	switch endpoint {
	case testEndpointOpenAIChat:
		targetModel, provider := db.ResolveModelWithProvider("gemini-3-flash")
		payload := map[string]interface{}{
			"model": "gemini-3-flash",
			"messages": []map[string]interface{}{
				{"role": "user", "content": "Say hello in one short sentence."},
			},
			"stream": false,
		}
		return runHandlerProbe(
			endpoint,
			"/v1/chat/completions",
			"gemini-3-flash",
			provider,
			targetModel,
			OpenAIChatHandler(tokenMgr, upstreamClient),
			payload,
			nil,
			false,
		), nil

	case testEndpointOpenAIResponses:
		targetModel, provider := db.ResolveModelWithProvider("gpt-5.2")
		payload := map[string]interface{}{
			"model": "gpt-5.2",
			"input": []map[string]interface{}{
				{
					"role": "user",
					"content": []map[string]interface{}{
						{"type": "input_text", "text": "Say hello in one short sentence."},
					},
				},
			},
			"stream": false,
		}
		return runHandlerProbe(
			endpoint,
			"/v1/responses",
			"gpt-5.2",
			provider,
			targetModel,
			OpenAIResponsesHandler(database, tokenMgr, upstreamClient),
			payload,
			nil,
			true,
		), nil

	case testEndpointAnthropic:
		payload := map[string]interface{}{
			"model":      "claude-sonnet-4-5",
			"max_tokens": 64,
			"messages": []map[string]interface{}{
				{"role": "user", "content": "Say hello in one short sentence."},
			},
		}
		return runHandlerProbe(
			endpoint,
			"/anthropic/v1/messages",
			"claude-sonnet-4-5",
			"google",
			db.ResolveModel("claude-sonnet-4-5", "google"),
			ClaudeMessagesHandler(tokenMgr, upstreamClient),
			payload,
			nil,
			false,
		), nil

	case testEndpointGenAIGenerate:
		targetModel, provider := "gemini-3-flash", "google"
		if resolvedModel, resolvedProvider, err := db.ResolveModelWithProviderForProtocol("gemini-3-flash", string(db.ProtocolGenAI)); err == nil {
			targetModel, provider = resolvedModel, resolvedProvider
		}
		payload := map[string]interface{}{
			"contents": []map[string]interface{}{
				{
					"role": "user",
					"parts": []map[string]interface{}{
						{"text": "Say hello in one short sentence."},
					},
				},
			},
		}
		return runHandlerProbe(
			endpoint,
			"/genai/v1beta/models/gemini-3-flash:generateContent",
			"gemini-3-flash",
			provider,
			targetModel,
			GenAIHandler(tokenMgr, upstreamClient),
			payload,
			map[string]string{"model": "gemini-3-flash"},
			false,
		), nil

	case testEndpointVertexGenerate:
		if VertexAIStudioProvider == nil || !VertexAIStudioProvider.IsEnabled() {
			return EndpointTestResult{
				Endpoint:    endpoint,
				Path:        "/v1/publishers/google/models/gemini-3-flash-preview:streamGenerateContent",
				Model:       "gemini-3-flash-preview",
				Provider:    "vertex",
				MappedModel: "gemini-3-flash-preview",
				StatusCode:  http.StatusOK,
				DurationMS:  0,
				ContentType: "application/json",
				Skipped:     true,
				Reason:      "vertex_proxy_disabled",
				Summary:     "Skipped because Vertex AI proxy is disabled",
				Snippet:     "",
			}, nil
		}

		payload := map[string]interface{}{
			"contents": []map[string]interface{}{
				{
					"role": "user",
					"parts": []map[string]interface{}{
						{"text": "Say hello in one short sentence."},
					},
				},
			},
		}
		return runHandlerProbe(
			endpoint,
			"/v1/publishers/google/models/gemini-3-flash-preview:streamGenerateContent",
			"gemini-3-flash-preview",
			"vertex",
			"gemini-3-flash-preview",
			VertexAIStudioProxyHandler(),
			payload,
			nil,
			true,
		), nil

	case testEndpointAIStudioGenerate:
		if GeminiAIStudioProvider == nil || !GeminiAIStudioProvider.IsEnabled() {
			return EndpointTestResult{
				Endpoint:    endpoint,
				Path:        "/v1beta/models/gemini-3-flash-preview:streamGenerateContent",
				Model:       "gemini-3-flash-preview",
				Provider:    "gemini",
				MappedModel: "gemini-3-flash-preview",
				StatusCode:  http.StatusOK,
				DurationMS:  0,
				ContentType: "application/json",
				Skipped:     true,
				Reason:      "gemini_api_proxy_disabled",
				Summary:     "Skipped because Gemini API proxy is disabled",
				Snippet:     "",
			}, nil
		}

		payload := map[string]interface{}{
			"contents": []map[string]interface{}{
				{
					"role": "user",
					"parts": []map[string]interface{}{
						{"text": "Say hello in one short sentence."},
					},
				},
			},
		}
		return runHandlerProbe(
			endpoint,
			"/v1beta/models/gemini-3-flash-preview:streamGenerateContent",
			"gemini-3-flash-preview",
			"gemini",
			"gemini-3-flash-preview",
			GeminiAIStudioProxyHandler(),
			payload,
			nil,
			true,
		), nil

	case testEndpointOpenRouterChat:
		return runOpenAICompatEndpointProbe(
			endpoint,
			"openrouter",
			"/openrouter/v1/chat/completions",
			"openrouter/free",
		), nil

	case testEndpointNVIDIAChat:
		return runOpenAICompatEndpointProbe(
			endpoint,
			"nvidia",
			"/nvidia/v1/chat/completions",
			"z-ai/glm4.7",
		), nil

	default:
		return EndpointTestResult{}, fmt.Errorf("Unsupported endpoint: %s", endpoint)
	}
}

func runOpenAICompatEndpointProbe(endpoint string, provider string, path string, model string) EndpointTestResult {
	info, ok := catalog.GetProvider(provider)
	if !ok || !info.Enabled || !info.RuntimeEnabled {
		return EndpointTestResult{
			Endpoint:    endpoint,
			Path:        path,
			Model:       model,
			Provider:    provider,
			MappedModel: model,
			StatusCode:  http.StatusOK,
			DurationMS:  0,
			ContentType: "application/json",
			Skipped:     true,
			Reason:      provider + "_proxy_disabled",
			Summary:     "Skipped because " + provider + " proxy is disabled",
			Snippet:     "",
		}
	}

	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Say hello in one short sentence."},
		},
		"stream": false,
	}
	return runHandlerProbe(
		endpoint,
		path,
		model,
		provider,
		model,
		OpenAICompatChatProxyHandler(),
		payload,
		map[string]string{"provider": provider},
		false,
	)
}

func runHandlerProbe(
	endpoint string,
	path string,
	model string,
	provider string,
	mappedModel string,
	handler http.HandlerFunc,
	payload map[string]interface{},
	routeParams map[string]string,
	allowResponsesSSE bool,
) EndpointTestResult {
	statusCode, contentType, body, duration := invokeHandler(handler, path, payload, routeParams)
	success := statusCode >= 200 && statusCode < 300

	if allowResponsesSSE && success {
		ct := strings.ToLower(contentType)
		success = strings.Contains(ct, "application/json") || strings.Contains(ct, "text/event-stream")
	}

	return EndpointTestResult{
		Endpoint:    endpoint,
		Path:        path,
		Model:       model,
		Provider:    provider,
		MappedModel: mappedModel,
		StatusCode:  statusCode,
		DurationMS:  duration,
		ContentType: contentType,
		Success:     success,
		Summary:     fmt.Sprintf("HTTP %d", statusCode),
		Snippet:     strings.TrimSpace(body),
	}
}

func invokeHandler(handler http.HandlerFunc, path string, payload map[string]interface{}, routeParams map[string]string) (int, string, string, int64) {
	bodyBytes, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	if len(routeParams) > 0 {
		chiCtx := chi.NewRouteContext()
		for k, v := range routeParams {
			chiCtx.URLParams.Add(k, v)
		}
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chiCtx))
	}

	rec := httptest.NewRecorder()
	start := time.Now()
	handler.ServeHTTP(rec, req)
	duration := time.Since(start).Milliseconds()

	res := rec.Result()
	defer res.Body.Close()
	respBody, _ := io.ReadAll(res.Body)

	return res.StatusCode, res.Header.Get("Content-Type"), string(respBody), duration
}

// ModelsHandler lists available models from upstream.
func ModelsHandler(tokenMgr *token.Manager, upstreamClient *upstream.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cachedToken, err := tokenMgr.GetToken("google")
		if err != nil {
			http.Error(w, "No valid token: "+err.Error(), http.StatusUnauthorized)
			return
		}

		resp, err := upstreamClient.FetchAvailableModels(cachedToken.AccessToken, cachedToken.ProjectID)
		if err != nil {
			http.Error(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, resp.Body)
	}
}
