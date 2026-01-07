package handlers

import (
	"os"
	"strings"
)

// IsVerbose checks if NEXUS_VERBOSE environment variable is set.
// Accepts: "1", "true", "yes" (case-insensitive)
func IsVerbose() bool {
	verbose := strings.ToLower(os.Getenv("NEXUS_VERBOSE"))
	return verbose == "1" || verbose == "true" || verbose == "yes"
}
