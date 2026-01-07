package handlers

import (
	"github.com/pysugar/oauth-llm-nexus/internal/util"
)

// IsVerbose checks if NEXUS_VERBOSE environment variable is set.
// Accepts: "1", "true", "yes" (case-insensitive)
// This re-exports util.IsVerbose for backward compatibility.
func IsVerbose() bool {
	return util.IsVerbose()
}
