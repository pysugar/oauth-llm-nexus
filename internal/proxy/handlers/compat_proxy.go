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

// genericCompatForwarder is the interface used by the generic transparent proxy.
type genericCompatForwarder interface {
	ForwardRequest(
		ctx context.Context,
		method string,
		subpath string,
		incomingQuery url.Values,
		incomingHeaders http.Header,
		body []byte,
	) (*http.Response, error)
}

// newGenericCompatForwarder is a factory var to allow injection in tests.
var newGenericCompatForwarder = func(info catalog.ProviderInfo, apiKey string, timeout time.Duration) genericCompatForwarder {
	return openaicompat.NewProvider(info.ID, apiKey, info.BaseURL, info.RootURL, timeout, info.StaticHeaders)
}

// GenericCompatProxyHandler returns an http.HandlerFunc that transparently forwards
// any request under /{provider}/* to the configured upstream, replacing only the
// Authorization header. The sub-path (everything after /{provider}) is appended to
// the provider's RootURL (BaseURL with version suffix stripped).
func GenericCompatProxyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		providerID := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "provider")))
		if providerID == "" {
			writeOpenAIError(w, "Missing provider path parameter", http.StatusBadRequest)
			return
		}

		// subpath = everything after /{providerID}, e.g. /v1/messages
		subpath := strings.TrimPrefix(r.URL.Path, "/"+providerID)
		if subpath == "" {
			subpath = "/"
		}

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			writeOpenAIError(w, "Failed to read request body", http.StatusBadRequest)
			return
		}

		requestID := GetOrGenerateRequestID(r)
		if !forwardGenericCompatRequest(w, r, providerID, subpath, bodyBytes, requestID) {
			writeOpenAIError(w, "Unknown OpenAI-compatible provider: "+providerID, http.StatusUnprocessableEntity)
		}
	}
}

// GenericCompatProxyHandlerWithMonitor wraps GenericCompatProxyHandler with request logging.
func GenericCompatProxyHandlerWithMonitor(pm *monitor.ProxyMonitor) http.HandlerFunc {
	baseHandler := GenericCompatProxyHandler()
	return func(w http.ResponseWriter, r *http.Request) {
		if pm == nil || !pm.IsEnabled() {
			baseHandler(w, r)
			return
		}

		startTime := time.Now()
		providerID := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "provider")))

		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

		// Extract model and stream flag – works for both OpenAI and Anthropic request bodies.
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

		inputTokens, outputTokens := extractUsageTokens(rec.body.String())
		errorMsg := ""
		respBody := rec.body.String()
		if rec.statusCode >= 400 {
			errorMsg = extractErrorMessage(respBody)
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

// forwardGenericCompatRequest does the actual transparent forwarding.
// Returns false if the provider is unknown/not registered.
func forwardGenericCompatRequest(
	w http.ResponseWriter,
	r *http.Request,
	providerID string,
	subpath string,
	bodyBytes []byte,
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

	proxy := newGenericCompatForwarder(info, apiKey, timeout)
	resp, err := proxy.ForwardRequest(r.Context(), r.Method, subpath, r.URL.Query(), r.Header, bodyBytes)
	if err != nil {
		writeOpenAIError(w, "Generic compat upstream error: "+err.Error(), http.StatusBadGateway)
		return true
	}
	defer resp.Body.Close()

	if err := openaicompat.CopyResponse(w, resp); err != nil {
		log.Printf("❌ [%s] Generic compat proxy response copy error: provider=%s path=%s err=%v", requestID, providerID, subpath, err)
	}
	return true
}

// extractUsageTokens parses token usage from either OpenAI or Anthropic response body.
// OpenAI:    prompt_tokens / completion_tokens
// Anthropic: input_tokens  / output_tokens
func extractUsageTokens(respBody string) (inputTokens, outputTokens int) {
	var openaiResp struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(respBody), &openaiResp) == nil && openaiResp.Usage.PromptTokens > 0 {
		return openaiResp.Usage.PromptTokens, openaiResp.Usage.CompletionTokens
	}

	var anthropicResp struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(respBody), &anthropicResp) == nil {
		return anthropicResp.Usage.InputTokens, anthropicResp.Usage.OutputTokens
	}
	return 0, 0
}

// extractErrorMessage extracts a human-readable error from an OpenAI or Anthropic error body.
func extractErrorMessage(respBody string) string {
	var openaiErr struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(respBody), &openaiErr) == nil && openaiErr.Error.Message != "" {
		return openaiErr.Error.Message
	}
	if len(respBody) < 500 {
		return respBody
	}
	return ""
}
