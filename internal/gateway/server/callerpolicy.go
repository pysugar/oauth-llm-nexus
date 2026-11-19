package server

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/clientkey"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/router"
)

// callerDenied builds the 403 returned when a client key's allowlist excludes a request.
func callerDenied(msg string) *ir.GatewayError {
	ge := ir.NewError(http.StatusForbidden, "forbidden_by_key_policy", msg)
	ge.Type = "permission_error"
	return ge
}

// checkCallerModel rejects client model aliases outside the caller's model allowlist.
func checkCallerModel(c *clientkey.Caller, model string) *ir.GatewayError {
	if c.AllowsModel(model) {
		return nil
	}
	return callerDenied(fmt.Sprintf("API key %q is not allowed to use model %q", c.Name, model))
}

// checkCallerDirect gates gateway paths that bypass the router and always hit providerID
// (e.g. Gemini countTokens/embedContent forwarding).
func checkCallerDirect(c *clientkey.Caller, providerID, model string) *ir.GatewayError {
	if ge := checkCallerModel(c, model); ge != nil {
		return ge
	}
	if !c.AllowsProvider(providerID) {
		return callerDenied(fmt.Sprintf("API key %q is not allowed to use provider %q", c.Name, providerID))
	}
	return nil
}

// applyCallerPolicy removes plan targets the caller may not reach: gateway candidates and the
// legacy OAuth delegation targets (google/codex/vertex). When only a permitted legacy fallback
// remains it is promoted to the primary delegate. It returns a 403 when nothing permitted is left.
func applyCallerPolicy(c *clientkey.Caller, plan *router.Plan) *ir.GatewayError {
	if plan == nil || c == nil || len(c.AllowedProviders) == 0 {
		return nil
	}
	var denied []string
	deny := func(id string) {
		if !slices.Contains(denied, id) {
			denied = append(denied, id)
		}
	}

	kept := make([]router.Candidate, 0, len(plan.Candidates))
	for _, cand := range plan.Candidates {
		if c.AllowsProvider(cand.Provider.ID) {
			kept = append(kept, cand)
		} else {
			deny(cand.Provider.ID)
		}
	}
	plan.Candidates = kept
	if plan.LegacyProvider != "" && !c.AllowsProvider(plan.LegacyProvider) {
		deny(plan.LegacyProvider)
		plan.LegacyProvider, plan.LegacyModel = "", ""
	}
	if plan.LegacyFallbackProvider != "" && !c.AllowsProvider(plan.LegacyFallbackProvider) {
		deny(plan.LegacyFallbackProvider)
		plan.LegacyFallbackProvider, plan.LegacyFallbackModel = "", ""
	}
	for _, id := range denied {
		plan.SkippedReasons = append(plan.SkippedReasons, id+": not allowed for API key "+c.Name)
	}

	if len(plan.Candidates) > 0 || plan.LegacyProvider != "" {
		return nil
	}
	if plan.LegacyFallbackProvider != "" {
		plan.LegacyProvider, plan.LegacyModel = plan.LegacyFallbackProvider, plan.LegacyFallbackModel
		plan.LegacyFallbackProvider, plan.LegacyFallbackModel = "", ""
		return nil
	}
	return callerDenied(fmt.Sprintf("API key %q is not allowed to use any backend resolved for model %q (resolved: %s; allowed: %s)",
		c.Name, plan.ClientModel, strings.Join(denied, ", "), strings.Join(c.AllowedProviders, ", ")))
}

func callerName(r *http.Request) string {
	if c := clientkey.FromContext(r.Context()); c != nil {
		return c.Name
	}
	return ""
}

// recordSuccess updates global/provider/model metrics and the calling key's counters.
func (s *Server) recordSuccess(r *http.Request, providerID, clientModel string, status int, latency time.Duration, usage ir.Usage) {
	s.Metrics.RecordSuccess(providerID, clientModel, status, latency, usage)
	s.Metrics.RecordKeySuccess(callerName(r), providerID, clientModel, status, latency, usage)
	s.recordUsage(r, providerID, clientModel, status, latency, usage, "")
}

// recordError updates global/provider/model metrics and the calling key's counters.
func (s *Server) recordError(r *http.Request, providerID, clientModel string, status int, latency time.Duration, errMsg string) {
	s.Metrics.RecordError(providerID, clientModel, status, latency, errMsg)
	s.Metrics.RecordKeyError(callerName(r), providerID, clientModel, status, latency, errMsg)
	s.recordUsage(r, providerID, clientModel, status, latency, ir.Usage{}, errMsg)
}
