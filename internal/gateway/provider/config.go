package provider

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/codec"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"gopkg.in/yaml.v3"
)

// ProviderFileConfig defines the YAML schema for config/providers.yaml.
type ProviderFileConfig struct {
	Providers []ProviderYAML `yaml:"providers"`
}

// ProviderYAML configures a single provider entry in providers.yaml.
type ProviderYAML struct {
	ID            string            `yaml:"id"`
	Preset        string            `yaml:"preset,omitempty"`
	DisplayName   string            `yaml:"display_name,omitempty"`
	Enabled       *bool             `yaml:"enabled,omitempty"`
	Endpoints     *Endpoints        `yaml:"endpoints,omitempty"`
	BaseURL       string            `yaml:"base_url,omitempty"`
	Native        []string          `yaml:"native,omitempty"`
	Prefer        string            `yaml:"prefer,omitempty"`
	AuthMode       string            `yaml:"auth_mode,omitempty"`
	KeyStrategy    string            `yaml:"key_strategy,omitempty"`
	NoKey          *bool             `yaml:"no_key,omitempty"`
	FreeOnly       *bool             `yaml:"free_only,omitempty"`
	IncludeNonChat *bool             `yaml:"include_non_chat,omitempty"`
	APIKey         string            `yaml:"api_key,omitempty"`
	APIKeys       []string          `yaml:"api_keys,omitempty"`
	StaticHeaders map[string]string `yaml:"static_headers,omitempty"`
	Timeout       string            `yaml:"timeout,omitempty"`
	Models        []string          `yaml:"models,omitempty"`
	ModelAliases  map[string]string `yaml:"model_aliases,omitempty"`
	Quirks        *QuirksYAML       `yaml:"quirks,omitempty"`
}

// QuirksYAML exposes codec quirk flags inside YAML config.
type QuirksYAML struct {
	MaxCompletionTokens   *bool `yaml:"max_completion_tokens,omitempty"`
	EchoReasoningContent  *bool `yaml:"echo_reasoning_content,omitempty"`
	IncludeUsageInStream  *bool `yaml:"include_usage_in_stream,omitempty"`
	OmitStreamOptions     *bool `yaml:"omit_stream_options,omitempty"`
	OmitParallelToolCalls *bool `yaml:"omit_parallel_tool_calls,omitempty"`
	OmitStrictTools       *bool `yaml:"omit_strict_tools,omitempty"`
	OmitReasoningEffort   *bool `yaml:"omit_reasoning_effort,omitempty"`
	ThoughtTagParsing     *bool `yaml:"thought_tag_parsing,omitempty"`
	StreamFirst           *bool `yaml:"stream_first,omitempty"`
}

// Registry holds all loaded gateway providers in priority order plus the shared model catalog.
type Registry struct {
	mu        sync.RWMutex
	order     []string
	byID      map[string]*Provider
	catalog   *Catalog
	httpClient *http.Client
}

// NewRegistry loads gateway provider configuration from YAML files and environment variables.
func NewRegistry() (*Registry, error) {
	httpClient := &http.Client{
		Timeout: 180 * time.Second,
	}
	reg := &Registry{
		byID:       make(map[string]*Provider),
		httpClient: httpClient,
	}
	reg.catalog = NewCatalog(httpClient)
	if err := reg.Reload(); err != nil {
		return reg, err
	}
	return reg, nil
}

// NewRegistryWithProviders builds a Registry directly from explicit providers (useful for unit tests).
func NewRegistryWithProviders(client *http.Client, providers ...*Provider) *Registry {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	reg := &Registry{
		byID:       make(map[string]*Provider, len(providers)),
		httpClient: client,
		catalog:    NewCatalog(client),
	}
	for _, p := range providers {
		if p == nil || p.ID == "" {
			continue
		}
		if p.Keys == nil {
			p.Keys = NewKeyRing(p.NoKey)
		}
		reg.byID[p.ID] = p
		reg.order = append(reg.order, p.ID)
	}
	return reg
}

// Reload reloads providers from config/providers.yaml, config/openai_compat_providers.yaml, and env vars.
func (r *Registry) Reload() error {
	providers, err := loadAllProviders()

	r.mu.Lock()
	r.order = r.order[:0]
	r.byID = make(map[string]*Provider, len(providers))
	for _, p := range providers {
		r.byID[p.ID] = p
		r.order = append(r.order, p.ID)
	}
	r.mu.Unlock()

	if r.catalog != nil {
		r.catalog.SetStaticModels(providers)
	}
	return err
}

// HTTPClient returns the shared HTTP client for upstream requests.
func (r *Registry) HTTPClient() *http.Client {
	return r.httpClient
}

// Catalog returns the provider model discovery catalog.
func (r *Registry) Catalog() *Catalog {
	return r.catalog
}

// Get looks up a provider by normalized ID.
func (r *Registry) Get(id string) (*Provider, bool) {
	norm := strings.ToLower(strings.TrimSpace(id))
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[norm]
	return p, ok
}

// Has reports whether id is registered in the gateway provider registry.
func (r *Registry) Has(id string) bool {
	_, ok := r.Get(id)
	return ok
}

// List returns all configured providers in priority order.
func (r *Registry) List() []*Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Provider, 0, len(r.order))
	for _, id := range r.order {
		if p, ok := r.byID[id]; ok {
			out = append(out, p)
		}
	}
	return out
}

// Available returns all enabled and credentialed providers in priority order.
func (r *Registry) Available() []*Provider {
	all := r.List()
	out := make([]*Provider, 0, len(all))
	for _, p := range all {
		if p.Available() {
			out = append(out, p)
		}
	}
	return out
}

func loadAllProviders() ([]*Provider, error) {
	var configs []ProviderYAML
	var loadErr error

	path := findConfigPath("NEXUS_PROVIDERS_FILE", filepath.Join("config", "providers.yaml"))
	if raw, err := os.ReadFile(path); err == nil {
		var fc ProviderFileConfig
		if uerr := yaml.Unmarshal(raw, &fc); uerr != nil {
			loadErr = uerr
		} else {
			configs = fc.Providers
		}
	}

	if len(configs) == 0 {
		for _, id := range DefaultPresetOrder {
			configs = append(configs, ProviderYAML{ID: id, Preset: id})
		}
	}

	seen := make(map[string]bool, len(configs))
	for _, c := range configs {
		seen[strings.ToLower(strings.TrimSpace(c.ID))] = true
	}

	// Backward-compatible import of any extra providers declared in config/openai_compat_providers.yaml.
	compatPath := findConfigPath("NEXUS_OPENAI_COMPAT_PROVIDERS_FILE", filepath.Join("config", "openai_compat_providers.yaml"))
	if raw, err := os.ReadFile(compatPath); err == nil {
		var legacy struct {
			Providers []ProviderYAML `yaml:"providers"`
		}
		if yaml.Unmarshal(raw, &legacy) == nil {
			for _, lp := range legacy.Providers {
				id := strings.ToLower(strings.TrimSpace(lp.ID))
				if id != "" && !seen[id] {
					seen[id] = true
					configs = append(configs, lp)
				}
			}
		}
	}

	result := make([]*Provider, 0, len(configs))
	for _, cfg := range configs {
		p := materializeProvider(cfg)
		if p != nil {
			result = append(result, p)
		}
	}
	return result, loadErr
}

func findConfigPath(envKey, defaultRel string) string {
	if custom := strings.TrimSpace(os.Getenv(envKey)); custom != "" {
		return custom
	}
	if _, err := os.Stat(defaultRel); err == nil {
		return defaultRel
	}
	// Try parent directories in case tests run inside a package subdirectory.
	for _, prefix := range []string{"../", "../../", "../../../"} {
		candidate := filepath.Join(prefix, defaultRel)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return defaultRel
}

func materializeProvider(cfg ProviderYAML) *Provider {
	id := strings.ToLower(strings.TrimSpace(cfg.ID))
	if id == "" {
		return nil
	}

	presetName := strings.ToLower(strings.TrimSpace(cfg.Preset))
	if presetName == "" {
		presetName = id
	}
	preset, hasPreset := GetPreset(presetName)

	p := &Provider{
		ID:            id,
		Preset:        presetName,
		DisplayName:   strings.ToUpper(id),
		Enabled:       true,
		AuthMode:      "bearer",
		Timeout:       180 * time.Second,
		StaticHeaders: make(map[string]string),
		ModelAliases:  make(map[string]string),
	}

	if hasPreset {
		p.DisplayName = preset.DisplayName
		p.Endpoints = preset.Endpoints
		p.Native = append([]ir.Protocol(nil), preset.Native...)
		p.Prefer = preset.Prefer
		p.AuthMode = preset.AuthMode
		p.KeyStrategy = preset.KeyStrategy
		p.NoKey = preset.NoKey
		p.FreeOnly = preset.FreeOnly
		p.IncludeNonChat = preset.IncludeNonChat
		p.Timeout = preset.Timeout
		p.Quirks = preset.Quirks
		for k, v := range preset.StaticHeaders {
			p.StaticHeaders[k] = v
		}
	}

	if cfg.DisplayName != "" {
		p.DisplayName = cfg.DisplayName
	}
	if cfg.Enabled != nil {
		p.Enabled = *cfg.Enabled
	}
	if cfg.NoKey != nil {
		p.NoKey = *cfg.NoKey
	}
	if cfg.FreeOnly != nil {
		p.FreeOnly = *cfg.FreeOnly
	}
	if cfg.IncludeNonChat != nil {
		p.IncludeNonChat = *cfg.IncludeNonChat
	}
	if cfg.KeyStrategy != "" {
		p.KeyStrategy = NormalizeKeyStrategy(cfg.KeyStrategy)
	}
	if cfg.AuthMode != "" {
		p.AuthMode = strings.ToLower(strings.TrimSpace(cfg.AuthMode))
		if p.AuthMode == "none" {
			p.NoKey = true
		}
	}
	if cfg.Endpoints != nil {
		if cfg.Endpoints.OpenAIChat != "" {
			p.Endpoints.OpenAIChat = expandEnvVal(cfg.Endpoints.OpenAIChat)
		}
		if cfg.Endpoints.OpenAIResponses != "" {
			p.Endpoints.OpenAIResponses = expandEnvVal(cfg.Endpoints.OpenAIResponses)
		}
		if cfg.Endpoints.Anthropic != "" {
			p.Endpoints.Anthropic = expandEnvVal(cfg.Endpoints.Anthropic)
		}
		if cfg.Endpoints.Gemini != "" {
			p.Endpoints.Gemini = expandEnvVal(cfg.Endpoints.Gemini)
		}
	}
	if cfg.BaseURL != "" && p.Endpoints.OpenAIChat == "" && p.Endpoints.Gemini == "" {
		p.Endpoints.OpenAIChat = expandEnvVal(cfg.BaseURL)
		if len(p.Native) == 0 {
			p.Native = []ir.Protocol{ir.ProtoOpenAIChat}
			p.Prefer = ir.ProtoOpenAIChat
		}
	}
	if len(cfg.Native) > 0 {
		var native []ir.Protocol
		for _, n := range cfg.Native {
			if pr, ok := parseProtocolName(n); ok {
				native = append(native, pr)
			}
		}
		if len(native) > 0 {
			p.Native = native
		}
	}
	if cfg.Prefer != "" {
		if pr, ok := parseProtocolName(cfg.Prefer); ok {
			p.Prefer = pr
		}
	}
	if len(p.Native) == 0 {
		p.Native = inferNativeFromEndpoints(p.Endpoints)
	}
	if p.Prefer == "" && len(p.Native) > 0 {
		p.Prefer = p.Native[0]
	}

	for k, v := range cfg.StaticHeaders {
		p.StaticHeaders[k] = expandEnvVal(v)
	}
	for k, v := range cfg.ModelAliases {
		p.ModelAliases[k] = v
	}
	if cfg.Timeout != "" {
		if dur, err := time.ParseDuration(cfg.Timeout); err == nil && dur > 0 {
			p.Timeout = dur
		}
	}
	if len(cfg.Models) > 0 {
		p.StaticModels = append([]string(nil), cfg.Models...)
	}
	if cfg.Quirks != nil {
		applyQuirksYAML(&p.Quirks, cfg.Quirks)
	}

	// Collect API keys from YAML and environment overrides.
	var keys []string
	if k := expandEnvVal(cfg.APIKey); k != "" {
		keys = append(keys, splitKeyList(k)...)
	}
	for _, k := range cfg.APIKeys {
		if ek := expandEnvVal(k); ek != "" {
			keys = append(keys, splitKeyList(ek)...)
		}
	}
	plainPrefix := strings.ToUpper(strings.ReplaceAll(id, "-", "_"))
	envPrefix := "NEXUS_" + plainPrefix
	for _, envVar := range []string{
		envPrefix + "_API_KEYS",
		plainPrefix + "_API_KEYS",
		envPrefix + "_API_KEY",
		plainPrefix + "_API_KEY",
	} {
		if envVal := strings.TrimSpace(os.Getenv(envVar)); envVal != "" {
			keys = append(keys, splitKeyList(envVal)...)
		}
	}

	// Apply URL/enable/free-only/key-strategy/timeout environment overrides.
	if v := strings.TrimSpace(os.Getenv(envPrefix + "_ENABLED")); v != "" {
		p.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := strings.TrimSpace(os.Getenv(envPrefix + "_FREE_ONLY")); v != "" {
		p.FreeOnly = strings.EqualFold(v, "true") || v == "1"
	}
	if v := strings.TrimSpace(os.Getenv(envPrefix + "_INCLUDE_NON_CHAT")); v != "" {
		p.IncludeNonChat = strings.EqualFold(v, "true") || v == "1"
	}
	if v := strings.TrimSpace(os.Getenv(envPrefix + "_KEY_STRATEGY")); v != "" {
		p.KeyStrategy = NormalizeKeyStrategy(v)
	}
	baseOverride := strings.TrimSpace(os.Getenv(envPrefix + "_BASE_URL"))
	if baseOverride == "" {
		baseOverride = strings.TrimSpace(os.Getenv(plainPrefix + "_BASE_URL"))
	}
	if baseOverride != "" {
		v := strings.TrimRight(baseOverride, "/")
		switch p.Prefer {
		case ir.ProtoGemini:
			p.Endpoints.Gemini = v
		case ir.ProtoAnthropic:
			p.Endpoints.Anthropic = v
		case ir.ProtoOpenAIResp:
			p.Endpoints.OpenAIResponses = ensureV1Suffix(v)
		default:
			v1URL := ensureV1Suffix(v)
			rootURL := strings.TrimSuffix(v1URL, "/v1")
			p.Endpoints.OpenAIChat = v1URL
			if id == "openai" || p.Endpoints.OpenAIResponses != "" {
				p.Endpoints.OpenAIResponses = v1URL
			}
			if p.Endpoints.Anthropic != "" && id != "deepseek" {
				p.Endpoints.Anthropic = rootURL
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv(envPrefix + "_ANTHROPIC_BASE_URL")); v != "" {
		p.Endpoints.Anthropic = strings.TrimRight(v, "/")
		p.Native = appendUniqueProtocol(p.Native, ir.ProtoAnthropic)
	}
	if v := strings.TrimSpace(os.Getenv(envPrefix + "_RESPONSES_BASE_URL")); v != "" {
		p.Endpoints.OpenAIResponses = strings.TrimRight(v, "/")
		p.Native = appendUniqueProtocol(p.Native, ir.ProtoOpenAIResp)
	}
	if v := strings.TrimSpace(os.Getenv(envPrefix + "_GEMINI_BASE_URL")); v != "" {
		p.Endpoints.Gemini = strings.TrimRight(v, "/")
		p.Native = appendUniqueProtocol(p.Native, ir.ProtoGemini)
	}
	if v := strings.TrimSpace(os.Getenv(envPrefix + "_TIMEOUT")); v != "" {
		if dur, err := time.ParseDuration(v); err == nil && dur > 0 {
			p.Timeout = dur
		}
	}

	p.Keys = NewKeyRingWithStrategy(p.NoKey, p.KeyStrategy, keys...)
	return p
}

func ensureV1Suffix(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || strings.HasSuffix(base, "/v1") {
		return base
	}
	return base + "/v1"
}

func appendUniqueProtocol(list []ir.Protocol, proto ir.Protocol) []ir.Protocol {
	for _, item := range list {
		if item == proto {
			return list
		}
	}
	return append(list, proto)
}

func expandEnvVal(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") && len(v) > 3 {
		return strings.TrimSpace(os.Getenv(v[2 : len(v)-1]))
	}
	return v
}

func splitKeyList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && ((raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'')) {
		raw = raw[1 : len(raw)-1]
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if len(trimmed) >= 2 && ((trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"') || (trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'')) {
			trimmed = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
		}
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseProtocolName(s string) (ir.Protocol, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "openai-chat", "openai.chat", "openai", "chat":
		return ir.ProtoOpenAIChat, true
	case "openai-responses", "openai.responses", "responses":
		return ir.ProtoOpenAIResp, true
	case "anthropic", "claude":
		return ir.ProtoAnthropic, true
	case "gemini", "google":
		return ir.ProtoGemini, true
	default:
		return "", false
	}
}

func inferNativeFromEndpoints(ep Endpoints) []ir.Protocol {
	var out []ir.Protocol
	if ep.OpenAIChat != "" {
		out = append(out, ir.ProtoOpenAIChat)
	}
	if ep.OpenAIResponses != "" {
		out = append(out, ir.ProtoOpenAIResp)
	}
	if ep.Anthropic != "" {
		out = append(out, ir.ProtoAnthropic)
	}
	if ep.Gemini != "" {
		out = append(out, ir.ProtoGemini)
	}
	return out
}

func applyQuirksYAML(dst *codec.Quirks, src *QuirksYAML) {
	if src.MaxCompletionTokens != nil {
		dst.MaxCompletionTokens = *src.MaxCompletionTokens
	}
	if src.EchoReasoningContent != nil {
		dst.EchoReasoningContent = *src.EchoReasoningContent
	}
	if src.IncludeUsageInStream != nil {
		dst.IncludeUsageInStream = *src.IncludeUsageInStream
	}
	if src.OmitStreamOptions != nil {
		dst.OmitStreamOptions = *src.OmitStreamOptions
	}
	if src.OmitParallelToolCalls != nil {
		dst.OmitParallelToolCalls = *src.OmitParallelToolCalls
	}
	if src.OmitStrictTools != nil {
		dst.OmitStrictTools = *src.OmitStrictTools
	}
	if src.OmitReasoningEffort != nil {
		dst.OmitReasoningEffort = *src.OmitReasoningEffort
	}
	if src.ThoughtTagParsing != nil {
		dst.ThoughtTagParsing = *src.ThoughtTagParsing
	}
	if src.StreamFirst != nil {
		dst.StreamFirst = *src.StreamFirst
	}
}
