package db

import (
	"fmt"
	"strings"
)

type RouteProtocol string

const (
	ProtocolOpenAI    RouteProtocol = "openai"
	ProtocolGenAI     RouteProtocol = "genai"
	ProtocolAnthropic RouteProtocol = "anthropic"
)

func AllowedProvidersByClientModel(clientModel string) []string {
	model := strings.ToLower(strings.TrimSpace(clientModel))

	switch {
	case strings.HasPrefix(model, "gpt"):
		return []string{"codex", "google"}
	case strings.HasPrefix(model, "gemini"):
		return []string{"google", "vertex", "gemini"}
	case strings.HasPrefix(model, "claude"):
		return []string{"google"}
	default:
		return []string{"google"}
	}
}

func AllowedProvidersByProtocol(protocol string) []string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case string(ProtocolOpenAI):
		return []string{"google", "codex"}
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
