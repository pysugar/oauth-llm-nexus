package anthropic

import (
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// claudeVersionRe reads "claude-opus-5-5", "claude-sonnet-4.6", "anthropic.claude-opus-5-5-…",
// and the older "claude-3-5-sonnet" spelling.
var claudeVersionRe = regexp.MustCompile(`claude-(?:(?:opus|sonnet|haiku|fable|mythos)-(\d+)(?:[-.](\d+))?|(\d+)(?:[-.](\d+))?-(?:opus|sonnet|haiku))`)

func isClaudeModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

func claudeVersion(model string) (major, minor int, ok bool) {
	m := claudeVersionRe.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return 0, 0, false
	}
	a, b := m[1], m[2]
	if a == "" {
		a, b = m[3], m[4]
	}
	major, _ = strconv.Atoi(a)
	if b != "" && len(b) <= 2 { // a date suffix (20250514) is not a minor version
		minor, _ = strconv.Atoi(b)
	}
	return major, minor, true
}

// AdaptiveOnly reports Claude models that refuse budget_tokens and think only adaptively
// (4.6 and later). NEXUS_ANTHROPIC_ADAPTIVE_MODELS adds comma-separated model prefixes.
func AdaptiveOnly(model string) bool {
	lower := strings.ToLower(model)
	for _, pfx := range strings.Split(os.Getenv("NEXUS_ANTHROPIC_ADAPTIVE_MODELS"), ",") {
		if pfx = strings.ToLower(strings.TrimSpace(pfx)); pfx != "" && strings.HasPrefix(lower, pfx) {
			return true
		}
	}
	major, minor, ok := claudeVersion(model)
	return ok && (major > 4 || (major == 4 && minor >= 6))
}

// adaptiveEffort maps an IR effort onto output_config.effort. 4.6 has no xhigh, so it gets max.
func adaptiveEffort(model, eff string) string {
	switch eff {
	case "none", "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	default: // xhigh, max
		if major, minor, ok := claudeVersion(model); ok && major == 4 && minor == 6 {
			return "max"
		}
		return "xhigh"
	}
}

// ValidClaudeSignature reports whether sig could have been issued by Claude.
func ValidClaudeSignature(sig string) bool { return codec.ValidClaudeSignature(sig) }
