package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/providers/catalog"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream/openaicompat"
)

type openAICompatForwarder interface {
	ForwardChatCompletions(
		ctx context.Context,
		method string,
		incomingQuery url.Values,
		incomingHeaders http.Header,
		body []byte,
	) (*http.Response, error)
}

var newOpenAICompatForwarder = func(info catalog.ProviderInfo, apiKey string, timeout time.Duration) openAICompatForwarder {
	return openaicompat.NewProvider(info.ID, apiKey, info.BaseURL, timeout, info.StaticHeaders)
}

func OpenAICompatChatProxyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerID := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "provider")))
		if providerID == "" {
			writeOpenAIError(w, "Missing provider path parameter", http.StatusBadRequest)
			return
		}

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			writeOpenAIError(w, "Failed to read request body", http.StatusBadRequest)
			return
		}

		requestID := GetOrGenerateRequestID(r)
		if !forwardOpenAICompatChat(w, r, providerID, bodyBytes, "", requestID) {
			writeOpenAIError(w, "Unknown OpenAI-compatible provider: "+providerID, http.StatusUnprocessableEntity)
		}
	}
}

func OpenAICompatChatProxyHandlerWithMonitor(pm *monitor.ProxyMonitor) http.HandlerFunc {
	baseHandler := OpenAICompatChatProxyHandler()
	return func(w http.ResponseWriter, r *http.Request) {
		if pm == nil || !pm.IsEnabled() {
			baseHandler(w, r)
			return
		}

		startTime := time.Now()
		providerID := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "provider")))

		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.Unmarshal(bodyBytes, &req)

		if req.Stream {
			sw := &streamSnippetRecorder{ResponseWriter: w, statusCode: http.StatusOK}
			baseHandler(sw, r)
			pm.LogRequest(models.RequestLog{
				Method:       r.Method,
				URL:          r.URL.Path,
				Status:       sw.statusCode,
				Duration:     time.Since(startTime).Milliseconds(),
				Provider:     providerID,
				Model:        req.Model,
				MappedModel:  req.Model,
				Error:        streamStatusError(sw.statusCode),
				RequestBody:  string(bodyBytes),
				ResponseBody: sw.Snippet(),
			})
			return
		}

		rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		baseHandler(rec, r)

		var inputTokens, outputTokens int
		var errorMsg string
		respBody := rec.body.String()
		if rec.statusCode >= 200 && rec.statusCode < 400 {
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
			Provider:     providerID,
			Model:        req.Model,
			MappedModel:  req.Model,
			Error:        errorMsg,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			RequestBody:  string(bodyBytes),
			ResponseBody: respBody,
		})
	}
}

func forwardOpenAICompatChat(
	w http.ResponseWriter,
	r *http.Request,
	providerID string,
	bodyBytes []byte,
	modelOverride string,
	requestID string,
) bool {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if !catalog.IsOpenAICompatProvider(providerID) {
		return false
	}

	info, apiKey, timeout, ok := catalog.GetRuntimeProvider(providerID)
	if !ok || !info.Enabled {
		writeOpenAIError(w, "OpenAI-compatible provider is disabled: "+providerID, http.StatusServiceUnavailable)
		return true
	}
	if !info.RuntimeEnabled {
		writeOpenAIError(w, fmt.Sprintf("OpenAI-compatible provider %s is not enabled (missing API key or base URL)", providerID), http.StatusServiceUnavailable)
		return true
	}

	payloadBytes := bodyBytes
	if strings.TrimSpace(modelOverride) != "" {
		var payload map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			writeOpenAIError(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
			return true
		}
		payload["model"] = strings.TrimSpace(modelOverride)
		rewritten, err := json.Marshal(payload)
		if err != nil {
			writeOpenAIError(w, "Failed to rewrite request body", http.StatusInternalServerError)
			return true
		}
		payloadBytes = rewritten
	}

	proxy := newOpenAICompatForwarder(info, apiKey, timeout)
	resp, err := proxy.ForwardChatCompletions(r.Context(), r.Method, r.URL.Query(), r.Header, payloadBytes)
	if err != nil {
		writeOpenAIError(w, "OpenAI-compatible upstream error: "+err.Error(), http.StatusBadGateway)
		return true
	}
	defer resp.Body.Close()

	if err := openaicompat.CopyResponse(w, resp); err != nil {
		log.Printf("❌ [%s] OpenAI-compatible proxy response copy error: provider=%s err=%v", requestID, providerID, err)
	}
	return true
}
