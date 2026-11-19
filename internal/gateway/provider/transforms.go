package provider

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
)

// IsUnredacted reports whether requests to this provider skip privacy masking (a local model).
func (p *Provider) IsUnredacted() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Unredacted
}

// GetExtraBody returns a copy of the provider's extra body fields.
func (p *Provider) GetExtraBody() map[string]json.RawMessage {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.ExtraBody) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(p.ExtraBody))
	for k, v := range p.ExtraBody {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

// GetSystemPrompts returns a copy of the provider's per-model appended system prompts.
func (p *Provider) GetSystemPrompts() map[string]string {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.SystemPrompts) == 0 {
		return nil
	}
	out := make(map[string]string, len(p.SystemPrompts))
	for k, v := range p.SystemPrompts {
		out[k] = v
	}
	return out
}

// SystemPromptFor is the system prompt appended for upstream model: the most specific matching
// pattern wins (an exact name, then the glob with the most literal characters; "*" matches any
// run of characters, "/" included, so "*" is every model).
func (p *Provider) SystemPromptFor(model string) string {
	prompts := p.GetSystemPrompts()
	best, bestScore := "", -1
	for pat, text := range prompts {
		if strings.TrimSpace(text) == "" {
			continue
		}
		score := -1
		if pat == model {
			score = 1 << 20
		} else if globMatch(pat, model) {
			score = len(pat) - strings.Count(pat, "*") - strings.Count(pat, "?")
		}
		if score > bestScore || score == bestScore && pat < best {
			best, bestScore = pat, score
		}
	}
	if bestScore < 0 {
		return ""
	}
	return prompts[best]
}

// ExtraBodyFromYAML converts YAML-decoded extra_body values to JSON, dropping (and logging)
// fields that can't be sent.
func ExtraBodyFromYAML(providerID string, in map[string]any) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	raw := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		b, err := json.Marshal(yamlToJSONValue(v))
		if err != nil {
			log.Printf("⚠️ provider %s: extra_body %q is not representable as JSON: %v", providerID, k, err)
			continue
		}
		raw[k] = b
	}
	out, dropped := codec.CleanExtraBody(raw)
	if len(dropped) > 0 {
		log.Printf("⚠️ provider %s: extra_body fields ignored (reserved, duplicate or invalid): %s", providerID, strings.Join(dropped, ", "))
	}
	return out
}

// yamlToJSONValue turns yaml.v3's map[string]any / map[any]any trees into JSON-encodable values.
func yamlToJSONValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = yamlToJSONValue(e)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if ks, ok := k.(string); ok {
				out[ks] = yamlToJSONValue(e)
			}
		}
		return out
	case []any:
		for i, e := range x {
			x[i] = yamlToJSONValue(e)
		}
		return x
	}
	return v
}

func cleanSystemPrompts(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" || strings.TrimSpace(v) == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// GetExtraBodyIf is a pointer to the provider's current extra body (never nil) when ok, else nil:
// for persisting the value a patch just set.
func (p *Provider) GetExtraBodyIf(ok bool) *map[string]json.RawMessage {
	if !ok {
		return nil
	}
	m := p.GetExtraBody()
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return &m
}

// GetSystemPromptsIf is GetExtraBodyIf for the per-model system prompts.
func (p *Provider) GetSystemPromptsIf(ok bool) *map[string]string {
	if !ok {
		return nil
	}
	m := p.GetSystemPrompts()
	if m == nil {
		m = map[string]string{}
	}
	return &m
}

// globMatch matches s against a pattern where * is any run of characters and ? one character.
func globMatch(pat, s string) bool {
	for len(pat) > 0 {
		switch pat[0] {
		case '*':
			for len(pat) > 0 && pat[0] == '*' {
				pat = pat[1:]
			}
			if pat == "" {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if globMatch(pat, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if s == "" {
				return false
			}
			pat, s = pat[1:], s[1:]
		default:
			if s == "" || pat[0] != s[0] {
				return false
			}
			pat, s = pat[1:], s[1:]
		}
	}
	return s == ""
}
