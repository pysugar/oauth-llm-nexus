// Package server implements the HTTP protocol gateway bridging unified frontend endpoints
// to multi-backend passthrough, protocol translation, key rotation, and failover.
package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/clientkey"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codecs"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/modelmeta"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/redact"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/router"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/usage"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
	"gorm.io/gorm"
)

const maxRequestBodyBytes = 32 * 1024 * 1024

// Server coordinates incoming inference requests across 4 frontend protocols and N backends.
type Server struct {
	Registry  *provider.Registry
	Router    *router.Router
	Cooldown  *router.Cooldown
	RespStore *ir.ResponseStore
	Metrics   *Metrics
	DB        *gorm.DB
	Monitor   *monitor.ProxyMonitor
	Legacy    LegacyHandlers

	// Stream overrides DefaultStreamPolicy (tests shrink the timers).
	Stream *StreamPolicy
	// Meta answers pricing, context windows and modalities (nil until EnableModelMeta).
	Meta *modelmeta.Registry
	// Ledger persists per-call usage (nil until EnableUsageLedger).
	Ledger *usage.Ledger

	caps    capState
	routing routingState
	masking *maskState

	pricesMu sync.RWMutex
	prices   map[string]modelmeta.Meta

	overridesMu sync.Mutex // serializes read-modify-write of persisted provider overrides
}

// New creates a gateway Server initialized with reg, database, proxy monitor, and legacy OAuth delegates.
func New(reg *provider.Registry, database *gorm.DB, pm *monitor.ProxyMonitor, legacy LegacyHandlers) *Server {
	cd := router.NewCooldown()
	rtr := router.New(reg, cd, database)
	rtr.LegacyAvailable = legacy.Available
	return &Server{
		Registry:  reg,
		Router:    rtr,
		Cooldown:  cd,
		RespStore: ir.NewResponseStore(0, 0),
		Metrics:   NewMetrics(),
		DB:        database,
		Monitor:   pm,
		Legacy:    legacy,
	}
}

// HandleOpenAIChat handles POST /v1/chat/completions.
func (s *Server) HandleOpenAIChat(w http.ResponseWriter, r *http.Request) {
	s.handleInference(w, r, ir.ProtoOpenAIChat, "", false, false)
}

// HandleOpenAIResponses handles POST /v1/responses.
func (s *Server) HandleOpenAIResponses(w http.ResponseWriter, r *http.Request) {
	s.handleInference(w, r, ir.ProtoOpenAIResp, "", false, false)
}

// HandleAnthropicMessages handles POST /anthropic/v1/messages and POST /v1/messages.
func (s *Server) HandleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	s.handleInference(w, r, ir.ProtoAnthropic, "", false, false)
}

// HandleGeminiModels handles GET/POST /v1beta/models and /v1beta/models/*.
func (s *Server) HandleGeminiModels(w http.ResponseWriter, r *http.Request) {
	modelID, action, isList, isModelGet := parseGeminiModelsPath(r.Method, r.URL.Path)
	if isList {
		s.HandleGeminiModelsList(w, r)
		return
	}
	if isModelGet {
		s.handleGeminiModelGet(w, r, modelID)
		return
	}

	switch action {
	case "generateContent":
		s.handleInference(w, r, ir.ProtoGemini, modelID, true, false)
	case "streamGenerateContent":
		s.handleInference(w, r, ir.ProtoGemini, modelID, true, true)
	default:
		// Non-generation actions (countTokens, embedContent, batchEmbedContents, etc.):
		// forward via configured gateway Gemini provider first (supports custom base URL & key ring),
		// falling back to the legacy Gemini AI Studio proxy if initialized. Both always reach Gemini.
		if ge := checkCallerDirect(clientkey.FromContext(r.Context()), "gemini", modelID); ge != nil {
			s.recordError(r, "", modelID, ge.HTTPStatus, 0, ge.Message) // denied before any upstream: not a provider error
			s.writeError(w, codecs.MustFor(ir.ProtoGemini), ge)
			return
		}
		if s.forwardGeminiProviderAction(w, r, modelID, action) {
			return
		}
		if s.Legacy.GeminiAIStudio != nil {
			s.Legacy.GeminiAIStudio.ServeHTTP(w, r)
			return
		}
		s.writeError(w, codecs.MustFor(ir.ProtoGemini),
			ir.NewError(501, "unsupported_action", "unsupported Gemini model action: "+action))
	}
}

func sanitizeUpstreamQuery(rawQuery string) string {
	if strings.TrimSpace(rawQuery) == "" {
		return ""
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}
	q.Del("key")
	return q.Encode()
}

func (s *Server) forwardGeminiProviderAction(w http.ResponseWriter, r *http.Request, modelID, action string) bool {
	if s.Registry == nil || modelID == "" || action == "" {
		return false
	}
	p, ok := s.Registry.Get("gemini")
	if !ok || !p.Available() {
		return false
	}
	base := strings.TrimRight(strings.TrimSpace(p.EndpointFor(ir.ProtoGemini)), "/")
	if base == "" {
		return false
	}
	body, err := readBodyLimited(r.Body, maxRequestBodyBytes)
	if err != nil {
		s.writeError(w, codecs.MustFor(ir.ProtoGemini), ir.NewError(400, "read_body_failed", err.Error()))
		return true
	}
	targetURL := base + "/models/" + url.PathEscape(modelID) + ":" + url.PathEscape(action)
	if cleanQuery := sanitizeUpstreamQuery(r.URL.RawQuery); cleanQuery != "" {
		targetURL += "?" + cleanQuery
	}

	maxKeys := 1
	if p.Keys != nil && p.Keys.Count() > 1 {
		maxKeys = p.Keys.Count()
	}
	tried := make(map[string]bool, maxKeys)
	key := ""
	if p.Keys != nil {
		key = p.Keys.Acquire()
	}

	for keyTry := 0; keyTry < maxKeys; keyTry++ {
		if key != "" {
			tried[key] = true
		}
		upReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(body))
		if err != nil {
			s.writeError(w, codecs.MustFor(ir.ProtoGemini), ir.NewError(500, "request_build_failed", err.Error()))
			return true
		}
		p.ApplyHeaders(upReq, ir.ProtoGemini, key)
		resp, err := s.Registry.HTTPClient().Do(upReq)
		if err != nil {
			s.writeError(w, codecs.MustFor(ir.ProtoGemini), ir.NewError(502, "upstream_transport_error", err.Error()))
			return true
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxRequestBodyBytes))
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			ge := codec.ParseUpstreamError(resp.StatusCode, respBody)
			act, classDur := router.ClassifyGatewayError(ge)
			if p.Keys != nil && key != "" {
				p.Keys.MarkFailureDetailed(key, resp.StatusCode, ge.Message, classDur)
			}
			if act.RotateKey && p.Keys != nil && keyTry+1 < maxKeys {
				if nextKey, rotated := p.Keys.NextHealthyExcluding(tried); rotated {
					key = nextKey
					continue
				}
			}
		} else if p.Keys != nil && key != "" {
			p.Keys.MarkSuccess(key)
		}

		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.Header().Set("X-Nexus-Provider", p.ID)
		w.Header().Set("X-Nexus-Upstream-Model", modelID)
		w.Header().Set("X-Nexus-Route", "passthrough")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		return true
	}
	return true
}

func (s *Server) handleInference(
	w http.ResponseWriter,
	r *http.Request,
	clientProto ir.Protocol,
	modelHint string,
	streamHintKnown bool,
	streamHint bool,
) {
	startTime := time.Now()
	clientCodec := codecs.MustFor(clientProto)
	r, ci := withCall(r, "chat", clientProto)

	rawBody, err := readBodyLimited(r.Body, maxRequestBodyBytes)
	if err != nil {
		s.writeError(w, clientCodec, ir.NewError(400, "read_body_failed", err.Error()))
		return
	}

	parse := func(body []byte) (*ir.Request, *ir.GatewayError) {
		req, ge := clientCodec.ParseRequest(modelHint, body)
		if ge == nil && streamHintKnown {
			req.Stream = streamHint
		}
		return req, ge
	}

	// Privacy masking runs on the client-protocol body before anything parses or forwards it, so
	// passthrough, translation and legacy delegation all see the masked body (fail closed).
	plainBody := rawBody
	maskedBody, hits, ge := s.maskBody(rawBody)
	if ge != nil {
		s.recordError(r, "", "", ge.HTTPStatus, time.Since(startTime), ge.Message)
		s.writeError(w, clientCodec, ge)
		return
	}
	rawBody = maskedBody
	var plain *plainRequest
	if redact.Masked(hits) > 0 {
		plain = &plainRequest{body: plainBody, parse: parse}
	}
	w, finishRestore := s.restoringWriter(w, hits)
	defer finishRestore()

	irReq, ge := parse(rawBody)
	if ge != nil {
		s.writeError(w, clientCodec, ge)
		return
	}
	ci.Session = sessionID(r, irReq)

	// Client key allowlists: the model alias is checked before routing, providers (including
	// legacy OAuth delegation targets) are filtered from the resolved plan before any upstream call.
	caller := clientkey.FromContext(r.Context())
	var plan *router.Plan
	ge = checkCallerModel(caller, irReq.Model)
	if ge == nil {
		plan, ge = s.Router.Resolve(r.Context(), irReq.Model, r.Header.Get("X-Nexus-Provider"))
	}
	if ge == nil {
		ge = applyCallerPolicy(caller, plan)
	}
	if ge == nil {
		ge = s.checkCallerCap(w, r)
	}
	if ge == nil {
		s.orderPlan(plan, irReq, ci.Session)
	}
	if ge != nil {
		s.recordError(r, "", irReq.Model, ge.HTTPStatus, time.Since(startTime), ge.Message)
		s.logMonitorError(r, clientProto, irReq.Model, "", rawBody, ge.HTTPStatus, startTime, ge.Message)
		s.writeError(w, clientCodec, ge)
		return
	}

	// Delegate to legacy OAuth handlers when resolved to google/codex/vertex.
	if plan.LegacyProvider != "" {
		r.Body = io.NopCloser(bytes.NewReader(rawBody))
		if s.delegateLegacy(w, r, clientProto, plan) {
			s.recordSuccess(r, plan.LegacyProvider, irReq.Model, http.StatusOK, time.Since(startTime), ir.Usage{})
			return
		}
		errMsg := ir.NewError(503, "legacy_provider_unavailable",
			"legacy OAuth provider "+plan.LegacyProvider+" is unavailable for protocol "+string(clientProto))
		s.recordError(r, plan.LegacyProvider, irReq.Model, errMsg.HTTPStatus, time.Since(startTime), errMsg.Message)
		s.logMonitorError(r, clientProto, irReq.Model, plan.LegacyProvider, rawBody, errMsg.HTTPStatus, startTime, errMsg.Message)
		s.writeError(w, clientCodec, errMsg)
		return
	}

	// Execute ordered gateway candidate attempts with pre-commit key rotation & failover.
	// Streaming clients share one clientStream across failover candidates so keepalives can be
	// sent while attempts are held, and a later candidate can stream into the same response.
	if irReq.Stream {
		w = newClientStream(w, clientProto, s.streamPolicy())
	}
	result, ge := s.executePlan(w, r, clientProto, clientCodec, irReq, rawBody, plain, plan, startTime)
	if ge != nil {
		// Fall back to legacy OAuth if all gateway candidates failed and a legacy candidate follows.
		if cs, ok := w.(*clientStream); ok && cs.headersSent {
			// Headers (keepalives) are out: a legacy handler cannot write its own response.
		} else if plan.LegacyFallbackProvider != "" {
			r.Body = io.NopCloser(bytes.NewReader(rawBody))
			fallbackPlan := &router.Plan{
				ClientModel:    plan.ClientModel,
				LegacyProvider: plan.LegacyFallbackProvider,
				LegacyModel:    plan.LegacyFallbackModel,
			}
			if s.delegateLegacy(w, r, clientProto, fallbackPlan) {
				s.recordSuccess(r, plan.LegacyFallbackProvider, irReq.Model, http.StatusOK, time.Since(startTime), ir.Usage{})
				return
			}
		}
		s.recordError(r, result.ProviderID, irReq.Model, ge.HTTPStatus, time.Since(startTime), ge.Message)
		s.logMonitorError(r, clientProto, irReq.Model, result.ProviderID, rawBody, ge.HTTPStatus, startTime, ge.Message)
		s.writeError(w, clientCodec, ge)
		return
	}

	s.observeRouting(ci.Session, irReq.Model, result.ProviderID, ir.Usage{
		InputTokens: result.InputTokens, OutputTokens: result.OutputTokens,
		CacheReadTokens: result.CachedTokens, CacheWriteTokens: result.CacheWriteTokens,
	})
	s.recordSuccess(r, result.ProviderID, irReq.Model, result.Status, time.Since(startTime), ir.Usage{
		InputTokens:      result.InputTokens,
		OutputTokens:     result.OutputTokens,
		CacheReadTokens:  result.CachedTokens,
		CacheWriteTokens: result.CacheWriteTokens,
		ReasoningTokens:  result.ReasoningTokens,
	})
	s.logMonitorSuccess(r, irReq.Model, result, rawBody, startTime)
}

func (s *Server) writeError(w http.ResponseWriter, c codec.Codec, ge *ir.GatewayError) {
	if ge == nil {
		ge = ir.NewError(500, "internal_error", "unknown error")
	}
	if cs, ok := w.(*clientStream); ok && cs.headersSent {
		cs.writeStreamError(c, ge)
		return
	}
	status := ge.HTTPStatus
	if status < 400 || status > 599 {
		status = 500
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(c.EncodeError(ge))
}

func readBodyLimited(rc io.ReadCloser, limit int64) ([]byte, error) {
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, limit))
}

func parseGeminiModelsPath(method, path string) (model string, action string, isList bool, isGetModel bool) {
	trimmed := strings.TrimPrefix(path, "/v1beta")
	trimmed = strings.TrimPrefix(trimmed, "/genai/v1beta")
	for strings.HasPrefix(trimmed, "/models") {
		next := strings.TrimPrefix(trimmed, "/models")
		if next == "" || next == "/" || strings.HasPrefix(next, "/") {
			trimmed = next
		} else {
			break
		}
	}
	if trimmed == "" || trimmed == "/" {
		if method == http.MethodGet {
			return "", "", true, false
		}
		return "", "", false, false
	}
	rest := strings.TrimPrefix(trimmed, "/")
	if decoded, err := url.PathUnescape(rest); err == nil {
		rest = decoded
	}
	if method == http.MethodGet {
		return rest, "", false, true
	}
	if colon := strings.LastIndexByte(rest, ':'); colon > 0 {
		return rest[:colon], rest[colon+1:], false, false
	}
	return rest, "", false, false
}

func (s *Server) handleGeminiModelGet(w http.ResponseWriter, r *http.Request, modelID string) {
	for _, p := range s.Registry.Available() {
		models := s.Registry.Catalog().EnsureFresh(r.Context(), p)
		for _, m := range models {
			if m.ID == modelID {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"name":        "models/" + m.ID,
					"displayName": m.ID,
					"provider":    p.ID,
				})
				return
			}
		}
	}
	if s.Legacy.GeminiAIStudio != nil {
		s.Legacy.GeminiAIStudio.ServeHTTP(w, r)
		return
	}
	s.writeError(w, codecs.MustFor(ir.ProtoGemini),
		ir.NewError(404, "model_not_found", "model "+modelID+" not found"))
}

type attemptOutcome struct {
	ProviderID       string
	UpstreamModel    string
	AccountLabel     string
	Status           int
	InputTokens      int
	OutputTokens     int
	CachedTokens     int
	ReasoningTokens  int
	ResponseBody     string
	CacheWriteTokens int
	TTFT             time.Duration
	StreamErr        string // set when a committed stream ended with an in-stream error
	KeyFP            string // fingerprint of the upstream key used
}

func (s *Server) logMonitorSuccess(r *http.Request, clientModel string, out attemptOutcome, rawBody []byte, start time.Time) {
	if s.Monitor == nil || !s.Monitor.IsEnabled() {
		return
	}
	status := out.Status
	if status == 0 {
		status = http.StatusOK
	}
	s.Monitor.LogRequest(models.RequestLog{
		Timestamp:    start.UnixMilli(),
		Method:       r.Method,
		URL:          r.URL.String(),
		Status:       status,
		Duration:     time.Since(start).Milliseconds(),
		Provider:     out.ProviderID,
		Model:        clientModel,
		MappedModel:  out.UpstreamModel,
		AccountEmail: out.AccountLabel,
		RequestBody:  string(rawBody),
		ResponseBody: out.ResponseBody,
		InputTokens:  out.InputTokens,
		OutputTokens: out.OutputTokens,
	})
}

func (s *Server) logMonitorError(r *http.Request, _ ir.Protocol, clientModel, provID string, rawBody []byte, status int, start time.Time, errMsg string) {
	if s.Monitor == nil || !s.Monitor.IsEnabled() {
		return
	}
	s.Monitor.LogRequest(models.RequestLog{
		Timestamp:   start.UnixMilli(),
		Method:      r.Method,
		URL:         r.URL.String(),
		Status:      status,
		Duration:    time.Since(start).Milliseconds(),
		Provider:    provID,
		Model:       clientModel,
		Error:       errMsg,
		RequestBody: string(rawBody),
	})
}
