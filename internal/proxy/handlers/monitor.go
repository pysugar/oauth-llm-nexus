package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
)

// GetRequestLogsHandler returns paginated request logs
func GetRequestLogsHandler(pm *monitor.ProxyMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
				limit = l
			}
		}

		logs := pm.GetLogs(limit)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"logs":  logs,
			"count": len(logs),
		})
	}
}

// GetRequestStatsHandler returns aggregated request statistics
func GetRequestStatsHandler(pm *monitor.ProxyMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats := pm.GetStats()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	}
}

// ClearRequestLogsHandler clears all request logs
func ClearRequestLogsHandler(pm *monitor.ProxyMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pm.Clear(); err != nil {
			http.Error(w, "Failed to clear logs: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}
}

// ToggleLoggingHandler enables or disables request logging
func ToggleLoggingHandler(pm *monitor.ProxyMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Enabled bool `json:"enabled"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		pm.SetEnabled(req.Enabled)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled": pm.IsEnabled(),
		})
	}
}

// GetLoggingStatusHandler returns the current logging status
func GetLoggingStatusHandler(pm *monitor.ProxyMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled": pm.IsEnabled(),
		})
	}
}
