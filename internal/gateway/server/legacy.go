package server

import (
	"net/http"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/router"
)

// LegacyHandlers holds existing OAuth/AI-Studio handlers delegated to for google/codex/vertex routes.
type LegacyHandlers struct {
	OpenAIChat        http.Handler
	OpenAIResponses   http.Handler
	AnthropicMessages http.Handler
	GeminiAIStudio    http.Handler
	Available         func(providerID string) bool
}

func (s *Server) delegateLegacy(w http.ResponseWriter, r *http.Request, clientProto ir.Protocol, plan *router.Plan) bool {
	if plan == nil || plan.LegacyProvider == "" {
		return false
	}
	// Mark legacy provider provenance headers unless response already written by delegate.
	w.Header().Set("X-Nexus-Provider", plan.LegacyProvider)
	if plan.LegacyModel != "" {
		w.Header().Set("X-Nexus-Upstream-Model", plan.LegacyModel)
	}
	w.Header().Set("X-Nexus-Route", "legacy-oauth")

	switch clientProto {
	case ir.ProtoOpenAIChat:
		if s.Legacy.OpenAIChat != nil {
			s.Legacy.OpenAIChat.ServeHTTP(w, r)
			return true
		}
	case ir.ProtoOpenAIResp:
		if s.Legacy.OpenAIResponses != nil {
			s.Legacy.OpenAIResponses.ServeHTTP(w, r)
			return true
		}
	case ir.ProtoAnthropic:
		if s.Legacy.AnthropicMessages != nil {
			s.Legacy.AnthropicMessages.ServeHTTP(w, r)
			return true
		}
	case ir.ProtoGemini:
		if s.Legacy.GeminiAIStudio != nil {
			s.Legacy.GeminiAIStudio.ServeHTTP(w, r)
			return true
		}
	}
	return false
}
