package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/clientkey"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/usage"
	"gorm.io/gorm/clause"
)

// capMemoTTL bounds how stale a spend sum may be; caps are soft by at most this window.
const capMemoTTL = 10 * time.Second

type capState struct {
	mu    sync.Mutex
	caps  map[string][]models.SpendCap // subject -> caps
	memo  map[string]capSpend          // subject|period -> spend
	ready bool
}

type capSpend struct {
	usd    float64
	tokens int64
	at     time.Time
}

func periodBounds(period string, now time.Time) (start, reset time.Time) {
	now = now.UTC()
	switch period {
	case "month":
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	default:
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 0, 1)
	}
}

func (s *Server) loadCaps() {
	if s.DB == nil {
		return
	}
	var rows []models.SpendCap
	if err := s.DB.Find(&rows).Error; err != nil {
		log.Printf("⚠️ failed to load spend caps: %v", err)
		return
	}
	m := make(map[string][]models.SpendCap)
	for _, c := range rows {
		m[c.Subject] = append(m[c.Subject], c)
	}
	s.caps.mu.Lock()
	s.caps.caps, s.caps.memo, s.caps.ready = m, map[string]capSpend{}, true
	s.caps.mu.Unlock()
}

func (s *Server) capsFor(subject string) []models.SpendCap {
	s.caps.mu.Lock()
	ready := s.caps.ready
	s.caps.mu.Unlock()
	if !ready {
		s.loadCaps()
	}
	s.caps.mu.Lock()
	defer s.caps.mu.Unlock()
	return s.caps.caps[subject]
}

func (s *Server) spend(subject, period string, start time.Time) capSpend {
	key := subject + "|" + period
	s.caps.mu.Lock()
	if m, ok := s.caps.memo[key]; ok && time.Since(m.at) < capMemoTTL {
		s.caps.mu.Unlock()
		return m
	}
	s.caps.mu.Unlock()

	var where string
	var args []any
	switch {
	case strings.HasPrefix(subject, "caller:"):
		where, args = "caller_name = ?", []any{strings.TrimPrefix(subject, "caller:")}
	case strings.HasPrefix(subject, "key:"):
		prov, fp, _ := strings.Cut(strings.TrimPrefix(subject, "key:"), "#")
		where, args = "provider = ? AND key_fp = ?", []any{prov, fp}
	default:
		return capSpend{at: time.Now()}
	}
	usd, tokens, err := usage.SpendSince(s.DB, start, s.priceUsage, where, args...)
	if err != nil {
		log.Printf("⚠️ spend query failed for %s: %v", subject, err)
	}
	m := capSpend{usd: usd, tokens: tokens, at: time.Now()}
	s.caps.mu.Lock()
	s.caps.memo[key] = m
	s.caps.mu.Unlock()
	return m
}

// capExceeded reports whether subject is over any of its caps, and when that cap resets.
func (s *Server) capExceeded(subject string) (bool, time.Time, string) {
	if s.DB == nil || s.Ledger == nil {
		return false, time.Time{}, ""
	}
	for _, c := range s.capsFor(subject) {
		start, reset := periodBounds(c.Period, time.Now())
		sp := s.spend(subject, c.Period, start)
		if c.USD > 0 && sp.usd >= c.USD {
			return true, reset, fmt.Sprintf("%s has used $%.6g of its $%.6g %s limit", subject, sp.usd, c.USD, c.Period)
		}
		if c.Tokens > 0 && sp.tokens >= c.Tokens {
			return true, reset, fmt.Sprintf("%s has used %d of its %d %s tokens", subject, sp.tokens, c.Tokens, c.Period)
		}
	}
	return false, time.Time{}, ""
}

// checkCallerCap refuses a request whose client key is over its spend cap with a 429 that says
// when it resets (Retry-After, X-Nexus-Limit-Reset).
func (s *Server) checkCallerCap(w http.ResponseWriter, r *http.Request) *ir.GatewayError {
	c := clientkey.FromContext(r.Context())
	if c == nil || c.Name == "" {
		return nil
	}
	over, reset, msg := s.capExceeded("caller:" + c.Name)
	if !over {
		return nil
	}
	secs := int(time.Until(reset).Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.Header().Set("X-Nexus-Limit-Reset", reset.Format(time.RFC3339))
	w.Header().Set("X-Should-Retry", "false")
	ge := ir.NewError(429, "gateway_key_limit", msg+"; resets at "+reset.Format(time.RFC3339))
	ge.Type = "rate_limit_error"
	return ge
}

// uncappedKey returns apiKey, or the next healthy key when apiKey is over its spend cap; ok=false
// when every key of the provider is capped (the attempt then fails over without cooldown).
func (s *Server) uncappedKey(p *provider.Provider, apiKey string, tried map[string]bool) (string, bool) {
	if apiKey == "" || p.Keys == nil || s.Ledger == nil {
		return apiKey, true
	}
	for {
		over, _, _ := s.capExceeded("key:" + p.ID + "#" + keyFingerprint(apiKey)[:12])
		if !over {
			return apiKey, true
		}
		tried[apiKey] = true
		next, ok := p.Keys.NextHealthyExcluding(tried)
		if !ok {
			return "", false
		}
		apiKey = next
	}
}

func errKeysCapped(providerID string) *ir.GatewayError {
	return ir.NewError(429, "upstream_key_limit", "every key of provider "+providerID+" is over its spend cap")
}

// HandleListCaps serves GET /api/gateway/caps with current spend per cap.
func (s *Server) HandleListCaps(w http.ResponseWriter, _ *http.Request) {
	if s.Ledger != nil {
		s.Ledger.Flush()
	}
	s.loadCaps()
	type capView struct {
		models.SpendCap
		SpentUSD    float64 `json:"spent_usd"`
		SpentTokens int64   `json:"spent_tokens"`
		ResetsAt    string  `json:"resets_at"`
	}
	var rows []models.SpendCap
	s.DB.Order("subject, period").Find(&rows)
	out := make([]capView, 0, len(rows))
	for _, c := range rows {
		start, reset := periodBounds(c.Period, time.Now())
		sp := s.spend(c.Subject, c.Period, start)
		out = append(out, capView{SpendCap: c, SpentUSD: sp.usd, SpentTokens: sp.tokens, ResetsAt: reset.Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"caps": out})
}

// HandlePutCap serves PUT /api/gateway/caps {subject, period, usd, tokens}.
func (s *Server) HandlePutCap(w http.ResponseWriter, r *http.Request) {
	var body models.SpendCap
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request JSON: " + err.Error()})
		return
	}
	body.Subject = strings.TrimSpace(body.Subject)
	if body.Period == "" {
		body.Period = "month"
	}
	if (!strings.HasPrefix(body.Subject, "caller:") && !strings.HasPrefix(body.Subject, "key:")) ||
		(body.Period != "day" && body.Period != "month") || body.USD < 0 || body.Tokens < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `subject must be "caller:<key name>" or "key:<provider>#<fp>", period day|month, limits >= 0`})
		return
	}
	body.ID = 0
	if err := s.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "subject"}, {Name: "period"}},
		DoUpdates: clause.AssignmentColumns([]string{"usd", "tokens"}),
	}).Create(&body).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.loadCaps()
	writeJSON(w, http.StatusOK, map[string]any{"cap": body})
}

// HandleDeleteCap serves DELETE /api/gateway/caps?subject=&period=.
func (s *Server) HandleDeleteCap(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res := s.DB.Where("subject = ? AND period = ?", q.Get("subject"), q.Get("period")).Delete(&models.SpendCap{})
	if res.Error != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": res.Error.Error()})
		return
	}
	s.loadCaps()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": res.RowsAffected})
}
