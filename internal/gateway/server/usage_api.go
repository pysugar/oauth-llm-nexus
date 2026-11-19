package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/usage"
)

// parseWhen accepts RFC 3339, unix milliseconds, or a duration back from now ("24h", "7d").
func parseWhen(v string, now time.Time) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, true
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.UnixMilli(ms), true
	}
	if strings.HasSuffix(v, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil {
			return now.Add(-time.Duration(n) * 24 * time.Hour), true
		}
	}
	if d, err := time.ParseDuration(v); err == nil {
		return now.Add(-d), true
	}
	return time.Time{}, false
}

// HandleUsage serves GET /api/gateway/usage?from=&to=&group_by=provider|model|caller|key|day
// [&caller=&provider=], with costs priced from model metadata. ?recent=N lists raw records.
func (s *Server) HandleUsage(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "usage ledger unavailable"})
		return
	}
	if s.Ledger != nil {
		s.Ledger.Flush()
	}
	q := r.URL.Query()
	now := time.Now()
	from, ok1 := parseWhen(q.Get("from"), now)
	to, ok2 := parseWhen(q.Get("to"), now)
	if !ok1 || !ok2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from/to must be RFC 3339, unix ms, or a duration like 24h / 7d"})
		return
	}
	if n, _ := strconv.Atoi(q.Get("recent")); n > 0 {
		if n > 500 {
			n = 500
		}
		var rows []models.UsageRecord
		s.DB.Order("ts DESC").Limit(n).Find(&rows)
		writeJSON(w, http.StatusOK, map[string]any{"records": rows})
		return
	}
	groupBy := q.Get("group_by")
	if groupBy == "" {
		groupBy = "provider"
	}
	sum, err := usage.Summarize(s.DB, usage.Query{From: from, To: to, GroupBy: groupBy, Caller: q.Get("caller"), Provider: q.Get("provider")}, s.priceUsage)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	resp := map[string]any{"summary": sum}
	if s.Ledger != nil {
		resp["dropped"] = s.Ledger.Dropped()
	}
	writeJSON(w, http.StatusOK, resp)
}
