package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	CapabilityOpenAIChat      = "openai.chat"
	CapabilityOpenAIResponses = "openai.responses"

	ModelScopeAllModels         = "all_models"
	ModelScopeUnknownPrefixOnly = "unknown_prefix_only"

	AuthModeBearer = "bearer"

	defaultTimeout = 180 * time.Second
)

var providerIDRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type fileConfig struct {
	Providers []ProviderConfig `yaml:"providers"`
}

type ProviderConfig struct {
	ID            string            `yaml:"id"`
	Enabled       *bool             `yaml:"enabled"`
	BaseURL       string            `yaml:"base_url"`
	AuthMode      string            `yaml:"auth_mode"`
	ModelScope    string            `yaml:"model_scope"`
	Capabilities  []string          `yaml:"capabilities"`
	StaticHeaders map[string]string `yaml:"static_headers"`
	Timeout       string            `yaml:"timeout"`
}

type ProviderInfo struct {
	ID             string            `json:"id"`
	Enabled        bool              `json:"enabled"`
	RuntimeEnabled bool              `json:"runtime_enabled"`
	BaseURL        string            `json:"base_url"`
	AuthMode       string            `json:"auth_mode"`
	ModelScope     string            `json:"model_scope"`
	Capabilities   []string          `json:"capabilities"`
	StaticHeaders  map[string]string `json:"static_headers,omitempty"`
	APIKeyEnv      string            `json:"api_key_env,omitempty"`
	BaseURLEnv     string            `json:"base_url_env,omitempty"`
}

type runtimeProvider struct {
	info    ProviderInfo
	apiKey  string
	timeout time.Duration
}

var (
	stateMu      sync.RWMutex
	initialized  bool
	providerByID map[string]runtimeProvider
	providerList []string
)

// InitFromEnvAndConfig initializes catalog by loading file and applying env overrides.
func InitFromEnvAndConfig() error {
	providers, err := loadProviders()

	stateMu.Lock()
	defer stateMu.Unlock()

	providerByID = make(map[string]runtimeProvider)
	providerList = providerList[:0]
	for _, p := range providers {
		providerByID[p.info.ID] = p
		providerList = append(providerList, p.info.ID)
	}
	initialized = true
	return err
}

func ensureInitialized() {
	stateMu.RLock()
	ok := initialized
	stateMu.RUnlock()
	if ok {
		return
	}
	_ = InitFromEnvAndConfig()
}

// ResetForTest resets in-memory state so tests can force reload.
func ResetForTest() {
	stateMu.Lock()
	defer stateMu.Unlock()
	initialized = false
	providerByID = nil
	providerList = nil
}

// GetProviders returns configured OpenAI-compatible providers.
func GetProviders() []ProviderInfo {
	ensureInitialized()

	stateMu.RLock()
	defer stateMu.RUnlock()

	result := make([]ProviderInfo, 0, len(providerList))
	for _, id := range providerList {
		entry, ok := providerByID[id]
		if !ok {
			continue
		}
		info := entry.info
		info.Capabilities = append([]string(nil), info.Capabilities...)
		if len(info.StaticHeaders) > 0 {
			cp := make(map[string]string, len(info.StaticHeaders))
			for k, v := range info.StaticHeaders {
				cp[k] = v
			}
			info.StaticHeaders = cp
		}
		result = append(result, info)
	}
	return result
}

// IsOpenAICompatProvider returns whether a provider is declared and enabled in config.
func IsOpenAICompatProvider(id string) bool {
	provider, ok := GetProvider(id)
	return ok && provider.Enabled
}

// GetProvider returns provider metadata by ID.
func GetProvider(id string) (ProviderInfo, bool) {
	ensureInitialized()

	stateMu.RLock()
	defer stateMu.RUnlock()

	entry, ok := providerByID[normalizeProviderID(id)]
	if !ok {
		return ProviderInfo{}, false
	}
	info := entry.info
	info.Capabilities = append([]string(nil), info.Capabilities...)
	if len(info.StaticHeaders) > 0 {
		cp := make(map[string]string, len(info.StaticHeaders))
		for k, v := range info.StaticHeaders {
			cp[k] = v
		}
		info.StaticHeaders = cp
	}
	return info, true
}

// GetRuntimeProvider returns provider runtime fields required for upstream calls.
func GetRuntimeProvider(id string) (ProviderInfo, string, time.Duration, bool) {
	ensureInitialized()

	stateMu.RLock()
	defer stateMu.RUnlock()

	entry, ok := providerByID[normalizeProviderID(id)]
	if !ok {
		return ProviderInfo{}, "", 0, false
	}
	info := entry.info
	info.Capabilities = append([]string(nil), info.Capabilities...)
	if len(info.StaticHeaders) > 0 {
		cp := make(map[string]string, len(info.StaticHeaders))
		for k, v := range info.StaticHeaders {
			cp[k] = v
		}
		info.StaticHeaders = cp
	}
	return info, entry.apiKey, entry.timeout, true
}

// ProviderIDsByCapability returns enabled provider IDs that declare a capability.
func ProviderIDsByCapability(capability string) []string {
	capability = strings.TrimSpace(strings.ToLower(capability))
	if capability == "" {
		return nil
	}

	providers := GetProviders()
	ids := make([]string, 0, len(providers))
	for _, p := range providers {
		if !p.Enabled {
			continue
		}
		for _, c := range p.Capabilities {
			if strings.EqualFold(strings.TrimSpace(c), capability) {
				ids = append(ids, p.ID)
				break
			}
		}
	}
	return ids
}

// SupportsCapability returns whether provider declares capability.
func SupportsCapability(providerID, capability string) bool {
	provider, ok := GetProvider(providerID)
	if !ok || !provider.Enabled {
		return false
	}
	capability = strings.TrimSpace(strings.ToLower(capability))
	for _, c := range provider.Capabilities {
		if strings.EqualFold(strings.TrimSpace(c), capability) {
			return true
		}
	}
	return false
}

// AllowedProviderIDsForModel returns enabled provider IDs that are selectable for a client model.
func AllowedProviderIDsForModel(clientModel string) []string {
	providers := GetProviders()
	ids := make([]string, 0, len(providers))
	for _, p := range providers {
		if !p.Enabled {
			continue
		}
		if isModelAllowedForScope(clientModel, p.ModelScope) {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

func isModelAllowedForScope(clientModel, modelScope string) bool {
	scope := strings.TrimSpace(strings.ToLower(modelScope))
	switch scope {
	case "", ModelScopeAllModels:
		return true
	case ModelScopeUnknownPrefixOnly:
		return !hasKnownPrefix(clientModel)
	default:
		return false
	}
}

func hasKnownPrefix(clientModel string) bool {
	m := strings.ToLower(strings.TrimSpace(clientModel))
	return strings.HasPrefix(m, "gpt") || strings.HasPrefix(m, "gemini") || strings.HasPrefix(m, "claude")
}

func loadProviders() ([]runtimeProvider, error) {
	cfgProviders, loadErr := loadConfigProviders()
	if len(cfgProviders) == 0 {
		cfgProviders = defaultProviders()
	}

	providers := make([]runtimeProvider, 0, len(cfgProviders))
	for _, cfg := range cfgProviders {
		runtimeEntry, ok := normalizeConfig(cfg)
		if !ok {
			continue
		}
		providers = append(providers, runtimeEntry)
	}

	sort.SliceStable(providers, func(i, j int) bool {
		return providers[i].info.ID < providers[j].info.ID
	})

	return providers, loadErr
}

func loadConfigProviders() ([]ProviderConfig, error) {
	path, err := resolveConfigPath()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read openai compat providers file %q: %w", path, err)
	}

	var cfg fileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse openai compat providers file %q: %w", path, err)
	}

	return cfg.Providers, nil
}

func resolveConfigPath() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("NEXUS_OPENAI_COMPAT_PROVIDERS_FILE")); explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", err
		}
		return explicit, nil
	}

	candidates := []string{
		"config/openai_compat_providers.yaml",
		"./config/openai_compat_providers.yaml",
		"/etc/nexus/openai_compat_providers.yaml",
		"/opt/homebrew/etc/nexus/openai_compat_providers.yaml",
		"/usr/local/etc/nexus/openai_compat_providers.yaml",
	}

	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		candidates = append(candidates,
			filepath.Join(homeDir, ".config", "nexus", "openai_compat_providers.yaml"),
			filepath.Join(homeDir, ".nexus", "openai_compat_providers.yaml"),
		)
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", nil
}

func normalizeConfig(cfg ProviderConfig) (runtimeProvider, bool) {
	id := normalizeProviderID(cfg.ID)
	if !providerIDRegexp.MatchString(id) {
		return runtimeProvider{}, false
	}

	enabled := true
	if cfg.Enabled != nil {
		enabled = *cfg.Enabled
	}

	authMode := strings.TrimSpace(strings.ToLower(cfg.AuthMode))
	if authMode == "" {
		authMode = AuthModeBearer
	}
	if authMode != AuthModeBearer {
		return runtimeProvider{}, false
	}

	modelScope := strings.TrimSpace(strings.ToLower(cfg.ModelScope))
	if modelScope == "" {
		modelScope = ModelScopeAllModels
	}

	capabilities := normalizeCapabilities(cfg.Capabilities)
	if len(capabilities) == 0 {
		capabilities = []string{CapabilityOpenAIChat}
	}

	baseURLEnv := providerEnvName(id, "BASE_URL")
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if v := strings.TrimSpace(os.Getenv(baseURLEnv)); v != "" {
		baseURL = v
	}

	apiKeyEnv := providerEnvName(id, "API_KEY")
	apiKey := strings.TrimSpace(os.Getenv(apiKeyEnv))

	staticHeaders := normalizeHeaders(cfg.StaticHeaders)
	if envHeaders := strings.TrimSpace(os.Getenv(providerEnvName(id, "STATIC_HEADERS"))); envHeaders != "" {
		fromEnv := map[string]string{}
		if err := json.Unmarshal([]byte(envHeaders), &fromEnv); err == nil {
			for k, v := range normalizeHeaders(fromEnv) {
				staticHeaders[k] = v
			}
		}
	}

	timeout := defaultTimeout
	if raw := strings.TrimSpace(cfg.Timeout); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			timeout = parsed
		}
	}
	if raw := strings.TrimSpace(os.Getenv(providerEnvName(id, "TIMEOUT"))); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			timeout = parsed
		}
	}

	info := ProviderInfo{
		ID:             id,
		Enabled:        enabled,
		RuntimeEnabled: enabled && strings.TrimSpace(baseURL) != "" && apiKey != "",
		BaseURL:        strings.TrimSpace(baseURL),
		AuthMode:       authMode,
		ModelScope:     modelScope,
		Capabilities:   capabilities,
		StaticHeaders:  staticHeaders,
		APIKeyEnv:      apiKeyEnv,
		BaseURLEnv:     baseURLEnv,
	}

	return runtimeProvider{info: info, apiKey: apiKey, timeout: timeout}, true
}

func normalizeCapabilities(capabilities []string) []string {
	if len(capabilities) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(capabilities))
	result := make([]string, 0, len(capabilities))
	for _, cap := range capabilities {
		normalized := strings.TrimSpace(strings.ToLower(cap))
		if normalized == "" {
			continue
		}
		if _, exists := set[normalized]; exists {
			continue
		}
		set[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func normalizeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return map[string]string{}
	}
	normalized := make(map[string]string, len(headers))
	for k, v := range headers {
		key := strings.TrimSpace(k)
		value := strings.TrimSpace(v)
		if key == "" || value == "" {
			continue
		}
		normalized[key] = value
	}
	return normalized
}

func normalizeProviderID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func providerEnvName(id, suffix string) string {
	upper := strings.ToUpper(id)
	replacer := strings.NewReplacer("-", "_", ".", "_", "/", "_", " ", "_")
	upper = replacer.Replace(upper)
	return fmt.Sprintf("NEXUS_%s_%s", upper, suffix)
}

func defaultProviders() []ProviderConfig {
	return []ProviderConfig{
		{
			ID:           "openrouter",
			Enabled:      boolPtr(true),
			BaseURL:      "https://openrouter.ai/api/v1",
			AuthMode:     AuthModeBearer,
			ModelScope:   ModelScopeAllModels,
			Capabilities: []string{CapabilityOpenAIChat},
		},
		{
			ID:           "nvidia",
			Enabled:      boolPtr(true),
			BaseURL:      "https://integrate.api.nvidia.com/v1",
			AuthMode:     AuthModeBearer,
			ModelScope:   ModelScopeUnknownPrefixOnly,
			Capabilities: []string{CapabilityOpenAIChat},
		},
	}
}

func boolPtr(v bool) *bool {
	return &v
}
