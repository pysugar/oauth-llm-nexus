package server

import (
	"fmt"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/router"
)

// thinkingRoomMaxLimit: only reply limits up to this size get thinking room added (magpie uses the
// same bound); a larger limit already leaves room to think.
const thinkingRoomMaxLimit = 16384

// thinksByDefault reports models that reason unless told not to and count that reasoning in the
// reply limit, so a small max_tokens can be spent entirely on thinking: Gemini 2.5/3/4, OpenAI's
// reasoning models, and DeepSeek's reasoner. Claude only thinks when asked, so it is not listed.
func thinksByDefault(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"):
		return false
	case strings.HasPrefix(m, "gemini-2.5"), strings.HasPrefix(m, "gemini-3"), strings.HasPrefix(m, "gemini-4"):
		return !strings.Contains(m, "image") && !strings.Contains(m, "tts") && !strings.Contains(m, "embedding")
	case strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"):
		return true
	case strings.HasPrefix(m, "gpt-5"):
		return !strings.Contains(m, "chat")
	case strings.Contains(m, "deepseek-reasoner"), strings.Contains(m, "deepseek-r1"), strings.HasPrefix(m, "qwq"), strings.HasSuffix(m, "-thinking"):
		return true
	}
	return false
}

// thinkingRoom returns the reply limit to send when a client named a small limit, asked for no
// reasoning of its own, and the target model thinks by default: the client's limit plus a high
// effort's thinking budget, within the model's own output limit when known. ok is false when the
// request goes as it came. (magpie: withThinkingRoom / replyRoom.)
func (s *Server) thinkingRoom(cand router.Candidate, req *ir.Request) (int, bool) {
	if req == nil || req.MaxTokens <= 0 || req.MaxTokens > thinkingRoomMaxLimit {
		return 0, false
	}
	if req.Reasoning.Raw || req.Reasoning.Effort != "" || req.Reasoning.Budget != 0 {
		return 0, false
	}
	if !thinksByDefault(cand.UpstreamModel) {
		return 0, false
	}
	limit := req.MaxTokens + ir.EffortToBudget("high")
	if meta, ok := s.lookupMeta(cand.Provider.ID, cand.UpstreamModel); ok && meta.MaxOutput > 0 && limit > meta.MaxOutput {
		limit = meta.MaxOutput
	}
	if limit <= req.MaxTokens {
		return 0, false
	}
	return limit, true
}

// explainEmptyReply adds what to do to an empty or cut-off reply when the client set a small limit.
func explainEmptyReply(ge *ir.GatewayError, req *ir.Request) *ir.GatewayError {
	if ge == nil || req == nil || req.MaxTokens <= 0 || req.MaxTokens > thinkingRoomMaxLimit {
		return ge
	}
	if ge.Code != "empty_reply" && ge.Code != "upstream_incomplete" {
		return ge
	}
	out := *ge
	out.Message = fmt.Sprintf("%s (max_tokens=%d may be too small: a thinking model can spend the whole limit on reasoning; raise max_tokens or lower the reasoning effort)",
		ge.Message, req.MaxTokens)
	return &out
}
