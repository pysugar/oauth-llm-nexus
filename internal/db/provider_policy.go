package db

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"github.com/pysugar/oauth-llm-nexus/internal/providers/catalog"
)

type RouteProtocol string

const (
	ProtocolOpenAI    RouteProtocol = "openai"
	ProtocolResponses RouteProtocol = "responses"
	ProtocolGenAI     RouteProtocol = "genai"
	ProtocolAnthropic RouteProtocol = "anthropic"
)

var (
	gatewayProvidersMu sync.RWMutex
	gatewayProviderIDs []string
)

// RegisterGatewayProviders updates the list of gateway provider IDs eligible across models and protocols.
func RegisterGatewayProviders(ids []string) {
	gatewayProvidersMu.Lock()
	defer gatewayProvidersMu.Unlock()
	gatewayProviderIDs = gatewayProviderIDs[:0]
	for _, id := range ids {
		norm := NormalizeProvider(id)
		if norm != "" && !slices.Contains(gatewayProviderIDs, norm) {
			gatewayProviderIDs = append(gatewayProviderIDs, norm)
		}
	}
}

func currentGatewayProviders() []string {
	gatewayProvidersMu.RLock()
	custom := append([]string(nil), gatewayProviderIDs...)
	gatewayProvidersMu.RUnlock()
	return mergeProviders(provider.DefaultPresetOrder, custom)
}

func AllowedProvidersByClientModel(clientModel string) []string {
	model := strings.ToLower(strings.TrimSpace(clientModel))
	var base []string

	switch {
	case strings.HasPrefix(model, "gpt"):
		base = []string{"codex", "google"}
	case strings.HasPrefix(model, "gemini"):
		base = []string{"google", "vertex", "gemini"}
	case strings.HasPrefix(model, "claude"):
		base = []string{"google"}
	default:
		base = []string{"google"}
	}

	compatProviders := catalog.AllowedProviderIDsForModel(model)
	return mergeProviders(base, append(compatProviders, currentGatewayProviders()...))
}

func AllowedProvidersByProtocol(protocol string) []string {
	gw := currentGatewayProviders()
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case string(ProtocolOpenAI), string(ProtocolResponses):
		base := []string{"google", "codex", "gemini"}
		compatProviders := catalog.ProviderIDsByCapability(catalog.CapabilityOpenAIChat)
		return mergeProviders(base, append(compatProviders, gw...))
	case string(ProtocolGenAI):
		return mergeProviders([]string{"google", "vertex", "gemini"}, gw)
	case string(ProtocolAnthropic):
		return mergeProviders([]string{"google"}, gw)
	default:
		return nil
	}
}

func ValidateRouteProvider(clientModel, targetProvider string) error {
	provider := NormalizeProvider(targetProvider)
	allowed := AllowedProvidersByClientModel(clientModel)
	if containsProvider(allowed, provider) {
		return nil
	}
	return fmt.Errorf(
		"provider %q is not allowed for client model %q (allowed: %s)",
		provider,
		clientModel,
		strings.Join(allowed, ", "),
	)
}

func ValidateProviderForProtocol(provider, protocol string) error {
	allowed := AllowedProvidersByProtocol(protocol)
	if len(allowed) == 0 {
		return fmt.Errorf("unsupported protocol %q", protocol)
	}
	p := NormalizeProvider(provider)
	if containsProvider(allowed, p) {
		return nil
	}
	return fmt.Errorf(
		"provider %q is not allowed for protocol %q (allowed: %s)",
		p,
		strings.ToLower(strings.TrimSpace(protocol)),
		strings.Join(allowed, ", "),
	)
}

// NormalizeProvider lower-cases and trims provider value.
// Empty input defaults to "google" as the product fallback provider.
func NormalizeProvider(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return "google"
	}
	return p
}

func containsProvider(allowed []string, provider string) bool {
	for _, p := range allowed {
		if p == provider {
			return true
		}
	}
	return false
}

func mergeProviders(base []string, extras []string) []string {
	result := make([]string, 0, len(base)+len(extras))
	for _, provider := range base {
		normalized := NormalizeProvider(provider)
		if normalized == "" || slices.Contains(result, normalized) {
			continue
		}
		result = append(result, normalized)
	}
	for _, provider := range extras {
		normalized := NormalizeProvider(provider)
		if normalized == "" || slices.Contains(result, normalized) {
			continue
		}
		result = append(result, normalized)
	}
	return result
}
