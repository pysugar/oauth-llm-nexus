package provider

import "strings"

var canonicalVendorPrefixes = []string{
	"deepseek/",
	"deepseek-ai/",
	"google/",
	"meta-llama/",
	"meta/",
	"qwen/",
	"mistralai/",
	"anthropic/",
	"openai/",
	"nvidia/",
	"microsoft/",
	"z-ai/",
	"thudm/",
	"01-ai/",
	"cohere/",
	"inclusionai/",
	"stepfun-ai/",
	"moonshotai/",
	"minimax/",
	"x-ai/",
	"perplexity/",
	"nousresearch/",
	"models/",
}

var canonicalTierSuffixes = []string{
	":free",
	":latest",
	":beta",
	":exacto",
	":nitro",
	":extended",
}

// CanonicalModelID normalizes vendor namespaces, routing suffixes (e.g. :free), casing,
// and well-known official aliases so equivalent models across providers group cleanly.
func CanonicalModelID(rawID string) string {
	s := strings.ToLower(strings.TrimSpace(rawID))
	if s == "" {
		return ""
	}

	// Strip iterative tier suffixes first (e.g., ":free").
	for {
		trimmed := s
		for _, sfx := range canonicalTierSuffixes {
			trimmed = strings.TrimSuffix(trimmed, sfx)
		}
		if trimmed == s {
			break
		}
		s = trimmed
	}

	// Strip known vendor or catalog namespaces iteratively.
	for {
		trimmed := s
		for _, pfx := range canonicalVendorPrefixes {
			if strings.HasPrefix(trimmed, pfx) && len(trimmed) > len(pfx) {
				trimmed = strings.TrimPrefix(trimmed, pfx)
			}
		}
		if trimmed == s {
			break
		}
		s = trimmed
	}

	switch s {
	case "deepseek-reasoner":
		return "deepseek-r1"
	}
	return s
}
