package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/clientkey"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codecs"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/router"
)

// HandleRerank serves POST /v1/rerank (and /rerank): the body Cohere, Jina and Voyage share
// (model, query, documents, top_n/top_k, return_documents) is passed through with only `model`
// rewritten, with routing, key rotation and failover like embeddings.
func (s *Server) HandleRerank(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	errCodec := codecs.MustFor(ir.ProtoOpenAIChat)
	r, _ = withCall(r, "rerank", ir.ProtoOpenAIChat)
	rawBody, err := readBodyLimited(r.Body, maxRequestBodyBytes)
	if err != nil {
		s.writeError(w, errCodec, ir.NewError(400, "read_body_failed", err.Error()))
		return
	}
	var req struct {
		Model     string          `json:"model"`
		Query     string          `json:"query"`
		Documents json.RawMessage `json:"documents"`
	}
	if err := json.Unmarshal(rawBody, &req); err != nil {
		s.writeError(w, errCodec, ir.NewError(400, "invalid_json", "invalid rerank request JSON: "+err.Error()))
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	if strings.TrimSpace(req.Query) == "" || len(req.Documents) == 0 || string(req.Documents) == "null" {
		s.writeError(w, errCodec, ir.NewError(400, "missing_input", "query and documents are required"))
		return
	}

	caller := clientkey.FromContext(r.Context())
	var plan *router.Plan
	ge := checkCallerModel(caller, req.Model)
	if ge == nil {
		plan, ge = s.Router.ResolveRerank(r.Context(), req.Model, r.Header.Get("X-Nexus-Provider"))
	}
	if ge == nil {
		ge = applyCallerPolicy(caller, plan)
	}
	if ge == nil {
		ge = s.checkCallerCap(w, r)
	}
	if ge != nil {
		s.recordError(r, "", req.Model, ge.HTTPStatus, time.Since(start), ge.Message)
		s.writeError(w, errCodec, ge)
		return
	}

	var lastErr *ir.GatewayError
	var lastProvider string
	for _, cand := range plan.Candidates {
		lastProvider = cand.Provider.ID
		payload, err := codec.RewriteModelJSON(rawBody, cand.UpstreamModel)
		if err != nil {
			lastErr = ir.NewError(400, "invalid_json", err.Error())
			break
		}
		endpoint := strings.TrimRight(strings.TrimSpace(cand.Provider.EndpointFor(ir.ProtoOpenAIChat)), "/") + "/rerank"
		body, attemptErr := s.postJSONWithKeys(r, cand, ir.ProtoOpenAIChat, endpoint, payload, plan.Forced)
		if attemptErr == nil {
			setProvenanceHeaders(w, cand, "passthrough")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			s.recordSuccess(r, cand.Provider.ID, req.Model, http.StatusOK, time.Since(start), ir.Usage{InputTokens: rerankTokens(body)})
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
		lastErr = ir.NewError(502, "all_attempts_failed", "all rerank backend candidates failed")
	}
	s.recordError(r, lastProvider, req.Model, lastErr.HTTPStatus, time.Since(start), lastErr.Message)
	s.writeError(w, errCodec, lastErr)
}

// rerankTokens reads usage from Voyage/Jina (usage.total_tokens) or Cohere (billed search units).
func rerankTokens(body []byte) int {
	var probe struct {
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
		Meta struct {
			BilledUnits struct {
				SearchUnits int `json:"search_units"`
			} `json:"billed_units"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(body, &probe)
	if probe.Usage.TotalTokens > 0 {
		return probe.Usage.TotalTokens
	}
	return probe.Meta.BilledUnits.SearchUnits
}
