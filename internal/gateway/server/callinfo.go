package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/clientkey"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/usage"
)

// callInfo collects what attempts learn about a call (served model, key, route, timing) so the
// single recording point can write a complete usage record.
type callInfo struct {
	Kind          string
	Proto         ir.Protocol
	UpstreamModel string
	KeyFP         string
	Route         string
	StreamErr     string
	Session       string
	TTFT          time.Duration
	CacheWrite    int
}

type callInfoKey struct{}

func withCall(r *http.Request, kind string, proto ir.Protocol) (*http.Request, *callInfo) {
	ci := &callInfo{Kind: kind, Proto: proto}
	return r.WithContext(context.WithValue(r.Context(), callInfoKey{}, ci)), ci
}

func callFrom(r *http.Request) *callInfo {
	if r == nil {
		return nil
	}
	ci, _ := r.Context().Value(callInfoKey{}).(*callInfo)
	return ci
}

// noteAttempt records the serving attempt's details on the request's callInfo.
func noteAttempt(r *http.Request, upstreamModel, apiKey, route string, out *attemptOutcome) {
	ci := callFrom(r)
	if ci == nil {
		return
	}
	ci.UpstreamModel = upstreamModel
	ci.Route = route
	if apiKey != "" {
		ci.KeyFP = keyFingerprint(apiKey)[:12]
	}
	if out != nil {
		ci.TTFT = out.TTFT
		ci.StreamErr = out.StreamErr
		ci.CacheWrite = out.CacheWriteTokens
	}
}

// sessionHeaders are conversation ids agents send (Claude Code, Codex, OpenCode, ...).
var sessionHeaders = []string{"X-Nexus-Session", "x-session-affinity", "x-session-id", "session_id", "x-claude-code-session-id", "x-conversation-id", "x-opencode-session"}

// sessionID identifies a conversation: an agent's session header, else a hash of the caller,
// model and first user message (stable across the conversation's turns).
func sessionID(r *http.Request, req *ir.Request) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	if req == nil {
		return ""
	}
	first := ""
	for _, m := range req.Messages {
		if m.Role == ir.RoleUser {
			if first = ir.TextOf(m.Parts); first != "" {
				break
			}
		}
	}
	if first == "" {
		return ""
	}
	caller := ""
	if c := clientkey.FromContext(r.Context()); c != nil {
		caller = c.Name
	}
	sum := sha256.Sum256([]byte(caller + "\x00" + req.Model + "\x00" + first))
	return "h-" + hex.EncodeToString(sum[:12])
}

// EnableUsageLedger persists every call to usage_records, pruning after retention (0 = keep).
func (s *Server) EnableUsageLedger(retention time.Duration) {
	if s.DB == nil {
		return
	}
	s.Ledger = usage.NewLedger(s.DB, retention)
}

// recordUsage writes the call to the ledger (no-op when the ledger is off).
func (s *Server) recordUsage(r *http.Request, providerID, clientModel string, status int, latency time.Duration, u ir.Usage, errMsg string) {
	if s.Ledger == nil {
		return
	}
	rec := models.UsageRecord{
		Provider:     providerID,
		ClientModel:  clientModel,
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		CacheRead:    u.CacheReadTokens,
		CacheWrite:   u.CacheWriteTokens,
		Reasoning:    u.ReasoningTokens,
		LatencyMS:    latency.Milliseconds(),
		Status:       status,
		Error:        truncate(errMsg, 300),
	}
	if c := clientkey.FromContext(r.Context()); c != nil {
		rec.CallerID, rec.CallerName = c.ID, c.Name
	}
	if ci := callFrom(r); ci != nil {
		rec.Kind, rec.Proto = ci.Kind, string(ci.Proto)
		rec.UpstreamModel, rec.KeyFP, rec.Route, rec.SessionID = ci.UpstreamModel, ci.KeyFP, ci.Route, ci.Session
		rec.TTFTMS = ci.TTFT.Milliseconds()
		if rec.CacheWrite == 0 {
			rec.CacheWrite = ci.CacheWrite
		}
		if ci.StreamErr != "" && rec.Error == "" {
			rec.Error = ci.StreamErr
		}
	}
	if r.Header.Get("X-Nexus-Test") != "" {
		rec.Route = "test"
	}
	s.Ledger.Record(rec)
}

// priceUsage prices u with the model metadata registry.
func (s *Server) priceUsage(providerID, model string, u ir.Usage) (float64, bool) {
	meta, ok := s.lookupMeta(providerID, model)
	if !ok || !meta.HasPrice {
		return 0, false
	}
	return meta.Cost(u), true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
