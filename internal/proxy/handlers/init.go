package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/version"
)

func init() {
	// Inject version into HTML templates at startup
	dashboardHTML = strings.ReplaceAll(dashboardHTML, "{{VERSION}}", version.Version)
	toolsPageHTML = strings.ReplaceAll(toolsPageHTML, "{{VERSION}}", version.Version)
	monitorPageHTML = strings.ReplaceAll(monitorPageHTML, "{{VERSION}}", version.Version)
	monitorHistoryHTML = strings.ReplaceAll(monitorHistoryHTML, "{{VERSION}}", version.Version)
}

// VersionHandler returns version information as JSON
// GET /api/version
func VersionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"version":    version.Version,
			"commit":     version.Commit,
			"build_time": version.BuildTime,
		})
	}
}
