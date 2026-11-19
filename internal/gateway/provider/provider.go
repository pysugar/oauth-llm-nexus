package provider

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

// Endpoints configures upstream base URLs per wire protocol for a provider.
type Endpoints struct {
	OpenAIChat      string `yaml:"openai_chat,omitempty" json:"openai_chat,omitempty"`
	OpenAIResponses string `yaml:"openai_responses,omitempty" json:"openai_responses,omitempty"`
	Anthropic       string `yaml:"anthropic,omitempty" json:"anthropic,omitempty"`
	Gemini          string `yaml:"gemini,omitempty" json:"gemini,omitempty"`
}

// Provider represents a configured upstream API backend.
type Provider struct {
	mu             sync.RWMutex
	ID             string
	Preset         string
	DisplayName    string
	Enabled        bool
	Endpoints      Endpoints
	Native         []ir.Protocol
	Prefer         ir.Protocol
	AuthMode       string
	KeyStrategy    string
	NoKey          bool
	FreeOnly       bool
	IncludeNonChat bool
	Keys           *KeyRing
	StaticHeaders  map[string]string
	Timeout        time.Duration // max wait for response headers, and max upstream idle gap while streaming
	// FirstTokenTimeout fails over when no text/thinking/tool call arrives in time (0 = off).
	FirstTokenTimeout time.Duration
	Quirks            codec.Quirks
	StaticModels      []string
	NoChat            bool // embeddings/rerank only (e.g. Voyage): never a chat/messages target
	ModelAliases      map[string]string
}

// IsEnabled safely reads whether the provider is currently enabled.
func (p *Provider) IsEnabled() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Enabled
}

// IsFreeOnly safely reads whether catalog discovery restricts this provider to zero-cost models.
func (p *Provider) IsFreeOnly() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.FreeOnly
}

// IsIncludeNonChat safely reads whether non-chat models (TTS, embeddings, etc.) are included in catalog discovery.
func (p *Provider) IsIncludeNonChat() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.IncludeNonChat
}

// PreferredProtocol safely reads the preferred translation target protocol.
func (p *Provider) PreferredProtocol() ir.Protocol {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Prefer
}

// GetKeyStrategy safely reads the configured key selection mode ("sticky" or "round-robin").
func (p *Provider) GetKeyStrategy() string {
	if p == nil {
		return KeyStrategySticky
	}
	if p.Keys != nil {
		return p.Keys.Strategy()
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return NormalizeKeyStrategy(p.KeyStrategy)
}

// RuntimePatchOptions groups thread-safe runtime modifications to a Provider.
type RuntimePatchOptions struct {
	Enabled        *bool
	FreeOnly       *bool
	IncludeNonChat *bool
	Prefer         *ir.Protocol
	KeyStrategy    *string
	AddAPIKey      string
	RemoveAPIKey   string
}

// ApplyRuntimePatch updates mutable runtime settings thread-safely and reports whether catalog needs refreshing.
func (p *Provider) ApplyRuntimePatch(enabled, freeOnly, includeNonChat *bool, prefer *ir.Protocol, addKey string) (catalogAffected bool, err error) {
	return p.ApplyRuntimePatchOptions(RuntimePatchOptions{
		Enabled:        enabled,
		FreeOnly:       freeOnly,
		IncludeNonChat: includeNonChat,
		Prefer:         prefer,
		AddAPIKey:      addKey,
	})
}

// ApplyRuntimePatchOptions updates mutable runtime settings, key ring entries, and key strategy thread-safely.
func (p *Provider) ApplyRuntimePatchOptions(opts RuntimePatchOptions) (catalogAffected bool, err error) {
	if p == nil {
		return false, fmt.Errorf("nil provider")
	}
	if opts.Prefer != nil && *opts.Prefer != "" && p.EndpointFor(*opts.Prefer) == "" {
		return false, fmt.Errorf("provider %s has no endpoint for protocol %s", p.ID, *opts.Prefer)
	}

	p.mu.Lock()
	if opts.Enabled != nil {
		p.Enabled = *opts.Enabled
	}
	if opts.FreeOnly != nil && p.FreeOnly != *opts.FreeOnly {
		p.FreeOnly = *opts.FreeOnly
		catalogAffected = true
	}
	if opts.IncludeNonChat != nil && p.IncludeNonChat != *opts.IncludeNonChat {
		p.IncludeNonChat = *opts.IncludeNonChat
		catalogAffected = true
	}
	if opts.Prefer != nil && *opts.Prefer != "" {
		p.Prefer = *opts.Prefer
	}
	if opts.KeyStrategy != nil && strings.TrimSpace(*opts.KeyStrategy) != "" {
		norm := NormalizeKeyStrategy(*opts.KeyStrategy)
		p.KeyStrategy = norm
		if p.Keys != nil {
			p.Keys.SetStrategy(norm)
		}
	}
	p.mu.Unlock()

	if k := strings.TrimSpace(opts.AddAPIKey); k != "" {
		if p.Keys == nil {
			p.Keys = NewKeyRingWithStrategy(false, p.KeyStrategy, k)
		} else {
			p.Keys.AddKeys(k)
		}
		catalogAffected = true
	}
	if rm := strings.TrimSpace(opts.RemoveAPIKey); rm != "" && p.Keys != nil {
		if p.Keys.RemoveByMasked(rm) {
			catalogAffected = true
		}
	}
	return catalogAffected, nil
}

// BaseURL returns the primary base URL for diagnostics and generic compat routing.
func (p *Provider) BaseURL() string {
	switch {
	case p.Endpoints.OpenAIChat != "":
		return p.Endpoints.OpenAIChat
	case p.Endpoints.OpenAIResponses != "":
		return p.Endpoints.OpenAIResponses
	case p.Endpoints.Anthropic != "":
		return p.Endpoints.Anthropic
	case p.Endpoints.Gemini != "":
		return p.Endpoints.Gemini
	default:
		return ""
	}
}

// EndpointFor returns the base URL configured for protocol proto.
func (p *Provider) EndpointFor(proto ir.Protocol) string {
	switch proto {
	case ir.ProtoOpenAIChat:
		return p.Endpoints.OpenAIChat
	case ir.ProtoOpenAIResp:
		return p.Endpoints.OpenAIResponses
	case ir.ProtoAnthropic:
		return p.Endpoints.Anthropic
	case ir.ProtoGemini:
		return p.Endpoints.Gemini
	default:
		return ""
	}
}

// HasCredentials returns true if the provider is keyless or has at least one API key configured.
func (p *Provider) HasCredentials() bool {
	if p == nil {
		return false
	}
	if p.NoKey || (p.Keys != nil && p.Keys.IsNoKey()) {
		return true
	}
	return p.Keys != nil && p.Keys.Count() > 0
}

// Available returns true when the provider is enabled and ready to serve requests.
func (p *Provider) Available() bool {
	return p != nil && p.IsEnabled() && p.HasCredentials()
}

// IsNative reports whether protocol proto is configured as a native passthrough target.
func (p *Provider) IsNative(proto ir.Protocol) bool {
	if p.EndpointFor(proto) == "" {
		return false
	}
	for _, n := range p.Native {
		if n == proto {
			return true
		}
	}
	return false
}

// TargetFor selects the upstream wire protocol and whether passthrough is valid for a client request protocol.
func (p *Provider) TargetFor(clientProto ir.Protocol) (target ir.Protocol, passthrough bool, ok bool) {
	if p == nil || p.NoChat {
		return "", false, false
	}
	if p.IsNative(clientProto) {
		return clientProto, true, true
	}
	if prefer := p.PreferredProtocol(); prefer != "" && p.EndpointFor(prefer) != "" {
		return prefer, false, true
	}
	for _, n := range p.Native {
		if p.EndpointFor(n) != "" {
			return n, false, true
		}
	}
	for _, candidate := range []ir.Protocol{ir.ProtoOpenAIChat, ir.ProtoAnthropic, ir.ProtoGemini, ir.ProtoOpenAIResp} {
		if p.EndpointFor(candidate) != "" {
			return candidate, candidate == clientProto, true
		}
	}
	return "", false, false
}

// MapModel rewrites clientModel using ModelAliases if configured.
func (p *Provider) MapModel(model string) string {
	if p == nil || len(p.ModelAliases) == 0 {
		return model
	}
	if mapped, ok := p.ModelAliases[model]; ok && mapped != "" {
		return mapped
	}
	return model
}

// EndpointURL builds the full upstream HTTP URL for the given protocol, model, and streaming flag.
func (p *Provider) EndpointURL(proto ir.Protocol, upstreamModel string, stream bool) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(p.EndpointFor(proto)), "/")
	if base == "" {
		return "", fmt.Errorf("provider %s has no endpoint for protocol %s", p.ID, proto)
	}
	switch proto {
	case ir.ProtoOpenAIChat:
		return base + "/chat/completions", nil
	case ir.ProtoOpenAIResp:
		return base + "/responses", nil
	case ir.ProtoAnthropic:
		if strings.HasSuffix(base, "/v1") {
			return base + "/messages", nil
		}
		return base + "/v1/messages", nil
	case ir.ProtoGemini:
		escaped := url.PathEscape(upstreamModel)
		action := ":generateContent"
		if stream {
			action = ":streamGenerateContent?alt=sse"
		}
		return base + "/models/" + escaped + action, nil
	default:
		return "", fmt.Errorf("unsupported protocol %q", proto)
	}
}

// ModelsListURL returns the URL used to discover available models for this provider.
func (p *Provider) ModelsListURL() (urlStr string, proto ir.Protocol) {
	if p.NoChat && len(p.StaticModels) > 0 {
		return "", "" // static-only provider without a /models listing
	}
	switch {
	case p.Endpoints.OpenAIChat != "":
		return strings.TrimRight(p.Endpoints.OpenAIChat, "/") + "/models", ir.ProtoOpenAIChat
	case p.Endpoints.Gemini != "":
		return strings.TrimRight(p.Endpoints.Gemini, "/") + "/models?pageSize=200", ir.ProtoGemini
	case p.Endpoints.OpenAIResponses != "":
		return strings.TrimRight(p.Endpoints.OpenAIResponses, "/") + "/models", ir.ProtoOpenAIResp
	case p.Endpoints.Anthropic != "":
		base := strings.TrimRight(p.Endpoints.Anthropic, "/")
		if strings.HasSuffix(base, "/v1") {
			return base + "/models", ir.ProtoAnthropic
		}
		return base + "/v1/models", ir.ProtoAnthropic
	default:
		return "", ""
	}
}

// ApplyHeaders injects authentication and static headers onto an outbound HTTP request.
func (p *Provider) ApplyHeaders(req *http.Request, targetProto ir.Protocol, apiKey string) {
	req.Header.Set("Content-Type", "application/json")
	for k, v := range p.StaticHeaders {
		req.Header.Set(k, v)
	}

	switch targetProto {
	case ir.ProtoAnthropic:
		req.Header.Set("anthropic-version", "2023-06-01")
		if apiKey != "" {
			if strings.EqualFold(p.AuthMode, "bearer") {
				req.Header.Set("Authorization", "Bearer "+apiKey)
				req.Header.Set("x-api-key", apiKey)
			} else {
				req.Header.Set("x-api-key", apiKey)
			}
		}
	case ir.ProtoGemini:
		if apiKey != "" {
			req.Header.Set("x-goog-api-key", apiKey)
		}
	default:
		if apiKey != "" {
			mode := strings.ToLower(strings.TrimSpace(p.AuthMode))
			switch mode {
			case "x-api-key":
				req.Header.Set("x-api-key", apiKey)
			case "x-goog-api-key":
				req.Header.Set("x-goog-api-key", apiKey)
			case "none":
				// Key provided explicitly despite AuthMode=none: fall back to Bearer.
				req.Header.Set("Authorization", "Bearer "+apiKey)
			default:
				req.Header.Set("Authorization", "Bearer "+apiKey)
			}
		}
	}
}
