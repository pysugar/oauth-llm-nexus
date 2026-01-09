package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
)

// GetRequestLogsHandler returns recent request logs (last 30 minutes, max 100)
func GetRequestLogsHandler(pm *monitor.ProxyMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
				limit = l
			}
		}

		// Default: last 30 minutes
		sinceMinutes := 30
		if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
			if s, err := strconv.Atoi(sinceStr); err == nil && s > 0 {
				sinceMinutes = s
			}
		}

		logs := pm.GetLogs(limit, sinceMinutes)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"logs":  logs,
			"count": len(logs),
		})
	}
}

// GetRequestLogsHistoryHandler returns paginated logs for history view
func GetRequestLogsHistoryHandler(pm *monitor.ProxyMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if pageStr := r.URL.Query().Get("page"); pageStr != "" {
			if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
				page = p
			}
		}

		pageSize := 100
		if sizeStr := r.URL.Query().Get("size"); sizeStr != "" {
			if s, err := strconv.Atoi(sizeStr); err == nil && s > 0 && s <= 500 {
				pageSize = s
			}
		}

		search := r.URL.Query().Get("q")

		logs, total := pm.GetLogsWithPagination(page, pageSize, search)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"logs":        logs,
			"page":        page,
			"page_size":   pageSize,
			"total":       total,
			"total_pages": (total + int64(pageSize) - 1) / int64(pageSize),
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
