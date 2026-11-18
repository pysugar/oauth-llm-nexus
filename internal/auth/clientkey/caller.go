// Package clientkey implements named, per-caller gateway API keys with optional provider and
// model allowlists. Keys are stored hashed; the caller resolved by auth middleware travels in the
// request context so the gateway can enforce the allowlists.
package clientkey

import (
	"context"
	"slices"
	"strings"
)

// LegacyName is the caller name reported for the single unrestricted configs.api_key credential.
const LegacyName = "legacy"

// Caller identifies the authenticated client of one request.
type Caller struct {
	ID               uint
	Name             string
	Legacy           bool     // the unrestricted configs.api_key credential
	AllowedProviders []string // normalized provider IDs; empty = any provider
	AllowedModels    []string // client model aliases, "prefix*" wildcards allowed; empty = any model
}

// LegacyCaller returns the unrestricted caller for the legacy gateway API key.
func LegacyCaller() *Caller {
	return &Caller{Name: LegacyName, Legacy: true}
}

// Restricted reports whether any allowlist applies to c.
func (c *Caller) Restricted() bool {
	return c != nil && (len(c.AllowedProviders) > 0 || len(c.AllowedModels) > 0)
}

// AllowsProvider reports whether c may reach the backend providerID. A nil caller (auth disabled)
// and callers without a provider allowlist may reach any provider.
func (c *Caller) AllowsProvider(providerID string) bool {
	if c == nil || len(c.AllowedProviders) == 0 {
		return true
	}
	return slices.Contains(c.AllowedProviders, NormalizeProvider(providerID))
}

// AllowsModel reports whether c may request the client-facing model alias. Matching is exact
// (case-sensitive, like route lookup) unless the pattern ends in "*", which matches by prefix.
func (c *Caller) AllowsModel(model string) bool {
	if c == nil || len(c.AllowedModels) == 0 {
		return true
	}
	model = strings.TrimSpace(model)
	for _, pat := range c.AllowedModels {
		if prefix, ok := strings.CutSuffix(pat, "*"); ok {
			if strings.HasPrefix(model, prefix) {
				return true
			}
			continue
		}
		if pat == model {
			return true
		}
	}
	return false
}

// NormalizeProvider canonicalizes a provider ID for allowlist comparison.
func NormalizeProvider(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

type ctxKey struct{}

// WithCaller returns ctx carrying c.
func WithCaller(ctx context.Context, c *Caller) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// FromContext returns the caller stored by WithCaller, or nil when the request is unauthenticated
// (auth disabled on first run) or did not pass through APIKeyAuth.
func FromContext(ctx context.Context) *Caller {
	c, _ := ctx.Value(ctxKey{}).(*Caller)
	return c
}
