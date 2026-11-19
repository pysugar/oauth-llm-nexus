package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/clientkey"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codecs"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/redact"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/router"
)

// geminiEmbedBatchLimit caps requests per Gemini :batchEmbedContents call.
const geminiEmbedBatchLimit = 100

// embeddingRequest is the OpenAI-compatible embeddings request plus the gateway's
// provider-neutral retrieval hint (`input_type`, alias `task_type`).
type embeddingRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	EncodingFormat string          `json:"encoding_format,omitempty"`
	Dimensions     int             `json:"dimensions,omitempty"`
	InputType      string          `json:"input_type,omitempty"`
	TaskType       string          `json:"task_type,omitempty"`
}

func (e *embeddingRequest) inputType() string {
	if v := strings.TrimSpace(e.InputType); v != "" {
		return v
	}
	return strings.TrimSpace(e.TaskType)
}

type embeddingResult struct {
	body         []byte
	promptTokens int
	routeLabel   string
}

// HandleOpenAIEmbeddings serves POST /v1/embeddings with model routing, key rotation and
// pre-response failover. OpenAI-compatible backends are passed through; Gemini backends are
// translated to :batchEmbedContents.
func (s *Server) HandleOpenAIEmbeddings(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	errCodec := codecs.MustFor(ir.ProtoOpenAIChat)
	r, ci := withCall(r, "embeddings", ir.ProtoOpenAIChat)

	rawBody, err := readBodyLimited(r.Body, maxRequestBodyBytes)
	if err != nil {
		s.writeError(w, errCodec, ir.NewError(400, "read_body_failed", err.Error()))
		return
	}
	var req embeddingRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		s.writeError(w, errCodec, ir.NewError(400, "invalid_json", "invalid embeddings request JSON: "+err.Error()))
		return
	}
	// Embeddings input is masked only when mask_embeddings is set: a placeholder embeds as a
	// different vector than the text it stands for, which changes retrieval.
	plainBody, plainReq := rawBody, req
	if s.maskSettings().MaskEmbeddings {
		masked, hits, ge := s.maskBody(rawBody)
		if ge != nil {
			s.recordError(r, "", req.Model, ge.HTTPStatus, time.Since(start), ge.Message)
			s.writeError(w, errCodec, ge)
			return
		}
		if redact.Masked(hits) > 0 {
			rawBody = masked
			req = embeddingRequest{}
			if err := json.Unmarshal(rawBody, &req); err != nil {
				s.writeError(w, errCodec, ir.NewError(503, "masking_unavailable", "masked embeddings request is not valid JSON"))
				return
			}
		}
	}
	req.Model = strings.TrimSpace(req.Model)
	plainReq.Model = req.Model
	if trimmed := bytes.TrimSpace(req.Input); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		s.writeError(w, errCodec, ir.NewError(400, "missing_input", "input is required"))
		return
	}

	// Client key allowlists apply before routing (model) and to the resolved plan (providers).
	caller := clientkey.FromContext(r.Context())
	var plan *router.Plan
	ge := checkCallerModel(caller, req.Model)
	if ge == nil {
		plan, ge = s.Router.ResolveEmbedding(r.Context(), req.Model, r.Header.Get("X-Nexus-Provider"))
	}
	if ge == nil {
		ge = applyCallerPolicy(caller, plan)
	}
	if ge == nil {
		ge = s.checkCallerCap(w, r)
	}
	if ge != nil {
		s.recordError(r, "", req.Model, ge.HTTPStatus, time.Since(start), ge.Message)
		s.logMonitorError(r, ir.ProtoOpenAIChat, req.Model, "", rawBody, ge.HTTPStatus, start, ge.Message)
		s.writeError(w, errCodec, ge)
		return
	}

	var lastErr *ir.GatewayError
	var lastProvider string
	for _, cand := range plan.Candidates {
		lastProvider = cand.Provider.ID
		candReq, candBody := &req, rawBody
		if cand.Provider.IsUnredacted() {
			candReq, candBody = &plainReq, plainBody
		}
		res, attemptErr := s.attemptEmbedding(r, cand, candReq, candBody, plan.Forced)
		if attemptErr == nil {
			ci.Route = res.routeLabel
			setProvenanceHeaders(w, cand, res.routeLabel)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(res.body)
			s.recordSuccess(r, cand.Provider.ID, req.Model, http.StatusOK, time.Since(start),
				ir.Usage{InputTokens: res.promptTokens})
			s.logMonitorSuccess(r, req.Model, attemptOutcome{
				ProviderID:    cand.Provider.ID,
				UpstreamModel: cand.UpstreamModel,
				Status:        http.StatusOK,
				InputTokens:   res.promptTokens,
			}, rawBody, start)
			return
		}
		lastErr = attemptErr
		if attemptErr.HTTPStatus == 499 {
			break
		}
		if action, _ := router.ClassifyGatewayError(attemptErr); !action.Failover || plan.Forced {
			break
		}
	}
	if lastErr == nil {
		lastErr = ir.NewError(502, "all_attempts_failed", "all embedding backend candidates failed")
	}
	s.recordError(r, lastProvider, req.Model, lastErr.HTTPStatus, time.Since(start), lastErr.Message)
	s.logMonitorError(r, ir.ProtoOpenAIChat, req.Model, lastProvider, rawBody, lastErr.HTTPStatus, start, lastErr.Message)
	s.writeError(w, errCodec, lastErr)
}

func (s *Server) attemptEmbedding(r *http.Request, cand router.Candidate, req *embeddingRequest, rawBody []byte, forced bool) (embeddingResult, *ir.GatewayError) {
	switch cand.Provider.EmbeddingProtocol() {
	case ir.ProtoOpenAIChat:
		payload, ge := buildOpenAIEmbeddingPayload(rawBody, cand)
		if ge != nil {
			return embeddingResult{}, ge
		}
		endpoint := strings.TrimRight(strings.TrimSpace(cand.Provider.EndpointFor(ir.ProtoOpenAIChat)), "/") + "/embeddings"
		body, ge := s.postJSONWithKeys(r, cand, ir.ProtoOpenAIChat, endpoint, payload, forced)
		if ge != nil {
			return embeddingResult{}, ge
		}
		var probe struct {
			Data  json.RawMessage `json:"data"`
			Usage struct {
				PromptTokens int `json:"prompt_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &probe); err != nil || len(probe.Data) == 0 {
			return embeddingResult{}, ir.NewError(502, "invalid_upstream_response",
				"provider "+cand.Provider.ID+" returned a non-embeddings payload")
		}
		return embeddingResult{body: body, promptTokens: probe.Usage.PromptTokens, routeLabel: "passthrough"}, nil

	case ir.ProtoGemini:
		return s.attemptGeminiEmbedding(r, cand, req, forced)

	default:
		return embeddingResult{}, ir.NewError(502, "embeddings_unsupported",
			"provider "+cand.Provider.ID+" has no embeddings-capable endpoint")
	}
}

// buildOpenAIEmbeddingPayload rewrites the upstream model and adapts the gateway `input_type`
// hint: forwarded (document → passage) for backends that accept it, stripped otherwise because
// strict OpenAI-compatible APIs reject unknown parameters.
func buildOpenAIEmbeddingPayload(rawBody []byte, cand router.Candidate) ([]byte, *ir.GatewayError) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &obj); err != nil {
		return nil, ir.NewError(400, "invalid_json", err.Error())
	}
	if cand.UpstreamModel != "" {
		mb, _ := json.Marshal(cand.UpstreamModel)
		obj["model"] = mb
	}
	hint := ""
	for _, k := range []string{"input_type", "task_type"} {
		if v, ok := obj[k]; ok {
			var sv string
			if json.Unmarshal(v, &sv) == nil && hint == "" {
				hint = strings.TrimSpace(sv)
			}
			delete(obj, k)
		}
	}
	voyage := cand.Provider.Preset == "voyage" || cand.Provider.ID == "voyage"
	if hint != "" && cand.Provider.Quirks.EmbeddingInputType {
		mapped := normalizePassageInputType(hint)
		if voyage && mapped == "passage" {
			mapped = "document" // Voyage names the passage side "document"
		}
		vb, _ := json.Marshal(mapped)
		obj["input_type"] = vb
	}
	if voyage {
		// Voyage spells OpenAI's dimensions output_dimension and takes only base64 or no format.
		if d, ok := obj["dimensions"]; ok {
			obj["output_dimension"] = d
			delete(obj, "dimensions")
		}
		if f, ok := obj["encoding_format"]; ok && strings.Trim(string(f), `" `) == "float" {
			delete(obj, "encoding_format")
		}
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, ir.NewError(500, "request_build_failed", err.Error())
	}
	return out, nil
}

func normalizePassageInputType(hint string) string {
	switch strings.ToLower(hint) {
	case "query", "search_query", "retrieval_query":
		return "query"
	case "passage", "document", "search_document", "retrieval_document":
		return "passage"
	default:
		return hint
	}
}

func geminiTaskType(hint string) string {
	switch strings.ToLower(strings.TrimSpace(hint)) {
	case "":
		return ""
	case "query", "search_query", "retrieval_query":
		return "RETRIEVAL_QUERY"
	case "passage", "document", "search_document", "retrieval_document":
		return "RETRIEVAL_DOCUMENT"
	case "similarity", "semantic_similarity":
		return "SEMANTIC_SIMILARITY"
	case "classification":
		return "CLASSIFICATION"
	case "clustering":
		return "CLUSTERING"
	case "question_answering":
		return "QUESTION_ANSWERING"
	case "fact_verification":
		return "FACT_VERIFICATION"
	case "code_retrieval_query":
		return "CODE_RETRIEVAL_QUERY"
	default:
		return strings.ToUpper(strings.TrimSpace(hint))
	}
}

func parseEmbeddingTextInputs(raw json.RawMessage) ([]string, *ir.GatewayError) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		if len(many) == 0 {
			return nil, ir.NewError(400, "missing_input", "input must not be empty")
		}
		return many, nil
	}
	return nil, ir.NewError(400, "unsupported_input",
		"token-array embedding inputs are only supported on OpenAI-compatible backends; send strings instead")
}

func (s *Server) attemptGeminiEmbedding(r *http.Request, cand router.Candidate, req *embeddingRequest, forced bool) (embeddingResult, *ir.GatewayError) {
	texts, ge := parseEmbeddingTextInputs(req.Input)
	if ge != nil {
		return embeddingResult{}, ge
	}
	model := strings.TrimPrefix(cand.UpstreamModel, "models/")
	base := strings.TrimRight(strings.TrimSpace(cand.Provider.EndpointFor(ir.ProtoGemini)), "/")
	endpoint := base + "/models/" + url.PathEscape(model) + ":batchEmbedContents"
	taskType := geminiTaskType(req.inputType())

	type part struct {
		Text string `json:"text"`
	}
	type content struct {
		Parts []part `json:"parts"`
	}
	type embedReq struct {
		Model                string  `json:"model"`
		Content              content `json:"content"`
		TaskType             string  `json:"taskType,omitempty"`
		OutputDimensionality int     `json:"outputDimensionality,omitempty"`
	}

	vectors := make([][]float64, 0, len(texts))
	for offset := 0; offset < len(texts); offset += geminiEmbedBatchLimit {
		end := offset + geminiEmbedBatchLimit
		if end > len(texts) {
			end = len(texts)
		}
		batch := make([]embedReq, 0, end-offset)
		for _, t := range texts[offset:end] {
			batch = append(batch, embedReq{
				Model:                "models/" + model,
				Content:              content{Parts: []part{{Text: t}}},
				TaskType:             taskType,
				OutputDimensionality: req.Dimensions,
			})
		}
		payload, _ := json.Marshal(map[string]any{"requests": batch})
		body, ge := s.postJSONWithKeys(r, cand, ir.ProtoGemini, endpoint, payload, forced)
		if ge != nil {
			return embeddingResult{}, ge
		}
		var resp struct {
			Embeddings []struct {
				Values []float64 `json:"values"`
			} `json:"embeddings"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || len(resp.Embeddings) != end-offset {
			return embeddingResult{}, ir.NewError(502, "invalid_upstream_response",
				"gemini batchEmbedContents returned an unexpected payload")
		}
		for _, e := range resp.Embeddings {
			vectors = append(vectors, e.Values)
		}
	}

	base64Out := strings.EqualFold(strings.TrimSpace(req.EncodingFormat), "base64")
	data := make([]map[string]any, 0, len(vectors))
	for i, v := range vectors {
		var emb any = v
		if base64Out {
			emb = encodeFloat32Base64(v)
		}
		data = append(data, map[string]any{"object": "embedding", "index": i, "embedding": emb})
	}
	out, _ := json.Marshal(map[string]any{
		"object": "list",
		"data":   data,
		"model":  model,
		"usage":  map[string]int{"prompt_tokens": 0, "total_tokens": 0},
	})
	return embeddingResult{body: out, routeLabel: "translated:" + string(ir.ProtoGemini)}, nil
}

// encodeFloat32Base64 matches OpenAI's `encoding_format=base64` (little-endian float32 array).
func encodeFloat32Base64(values []float64) string {
	buf := make([]byte, 4*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(v)))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// postJSONWithKeys performs a non-streaming POST against cand with per-key rotation, key/provider
// cooldown bookkeeping and client-cancel detection, returning the 2xx response body.
func (s *Server) postJSONWithKeys(r *http.Request, cand router.Candidate, proto ir.Protocol, endpoint string, payload []byte, forced bool) ([]byte, *ir.GatewayError) {
	p := cand.Provider
	maxKeys := 1
	if p.Keys != nil && p.Keys.Count() > 1 {
		maxKeys = p.Keys.Count()
	}
	tried := make(map[string]bool, maxKeys)
	apiKey := ""
	if p.Keys != nil {
		apiKey = p.Keys.Acquire()
	}

	var lastErr *ir.GatewayError
	for keyTry := 0; keyTry < maxKeys; keyTry++ {
		var uncapped bool
		if apiKey, uncapped = s.uncappedKey(p, apiKey, tried); !uncapped {
			return nil, errKeysCapped(p.ID)
		}
		if apiKey != "" {
			tried[apiKey] = true
		}
		ctx := r.Context()
		cancel := context.CancelFunc(func() {})
		if p.Timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, p.Timeout)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			cancel()
			return nil, ir.NewError(500, "request_build_failed", err.Error())
		}
		p.ApplyHeaders(httpReq, proto, apiKey)
		resp, err := s.Registry.HTTPClient().Do(httpReq)
		if err != nil {
			cancel()
			if errors.Is(r.Context().Err(), context.Canceled) {
				return nil, ir.NewError(499, "request_canceled", "client canceled request")
			}
			ge := ir.NewError(502, "upstream_transport_error", err.Error())
			s.Cooldown.MarkFailure(p.ID, ge.HTTPStatus, err.Error())
			return nil, ge
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRequestBodyBytes))
		resp.Body.Close()
		cancel()
		hints := router.ParseRateLimitHeaders(resp.Header, time.Now())

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			ge := codec.ParseUpstreamError(resp.StatusCode, body)
			action, classDur := router.ClassifyGatewayError(ge)
			keyDur := hints.RetryAfter
			if keyDur <= 0 {
				keyDur = classDur
			}
			if p.Keys != nil && apiKey != "" {
				p.Keys.MarkFailureDetailed(apiKey, resp.StatusCode, ge.Message, keyDur)
			}
			if action.RotateKey && p.Keys != nil && keyTry+1 < maxKeys {
				if next, ok := p.Keys.NextHealthyExcluding(tried); ok {
					apiKey = next
					lastErr = ge
					continue
				}
			}
			if action.Cooldown && (!forced || resp.StatusCode >= 500) && (p.Keys == nil || p.Keys.HealthyCount() == 0) {
				s.Cooldown.MarkFailureWithRetry(p.ID, resp.StatusCode, ge.Message, hints.RetryAfter)
			}
			return nil, ge
		}
		if readErr != nil {
			return nil, ir.NewError(502, "upstream_read_failed", readErr.Error())
		}
		noteAttempt(r, cand.UpstreamModel, apiKey, "passthrough", nil)
		if p.Keys != nil {
			if hints.ProactivelyExhausted && p.Keys.Count() > 1 {
				p.Keys.MarkExhaustedAndRotate(apiKey, hints.RetryAfter)
			} else {
				p.Keys.MarkSuccess(apiKey)
			}
		}
		s.Cooldown.RecordSuccess(p.ID)
		return body, nil
	}
	return nil, lastErr
}
