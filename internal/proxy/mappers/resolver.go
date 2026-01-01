package mappers

import (
	"github.com/pysugar/oauth-llm-nexus/internal/db"
)

// ResolveModel resolves client model to target model for the specified backend provider
// Currently only "google" backend is supported
// Falls back to passthrough if no mapping exists
func ResolveModel(clientModel, targetProvider string) string {
	return db.ResolveModel(clientModel, targetProvider)
}

// ResolveModelForGoogle is a convenience function for Google backend
func ResolveModelForGoogle(clientModel string) string {
	return db.ResolveModel(clientModel, "google")
}
