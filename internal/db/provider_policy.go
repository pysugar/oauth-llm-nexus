package db

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/providers/catalog"
)

type RouteProtocol string

const (
	ProtocolOpenAI    RouteProtocol = "openai"
	ProtocolGenAI     RouteProtocol = "genai"
	ProtocolAnthropic RouteProtocol = "anthropic"
)

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
	return mergeProviders(base, compatProviders)
}

func AllowedProvidersByProtocol(protocol string) []string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case string(ProtocolOpenAI):
		base := []string{"google", "codex"}
		compatProviders := catalog.ProviderIDsByCapability(catalog.CapabilityOpenAIChat)
		return mergeProviders(base, compatProviders)
	case string(ProtocolGenAI):
		return []string{"google", "vertex", "gemini"}
	case string(ProtocolAnthropic):
		return []string{"google"}
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
