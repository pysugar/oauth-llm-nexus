// Package provider manages upstream AI API backends, credentials, presets, and model catalogs.
package provider

import (
	"math"
	"strings"
	"sync"
	"time"
)

const (
	// KeyStrategySticky uses the first healthy key until a cooldown forces rotation.
	KeyStrategySticky = "sticky"
	// KeyStrategyRoundRobin distributes consecutive acquisitions across healthy keys.
	KeyStrategyRoundRobin = "round-robin"
)

type keyMetric struct {
	requests   int64
	errors     int64
	lastStatus int
	lastError  string
	lastUsedAt time.Time
}

// KeyStatus describes runtime health and telemetry for a single API key slot in a KeyRing.
type KeyStatus struct {
	Index           int    `json:"index"`
	Masked          string `json:"masked"`
	Active          bool   `json:"active"`
	Healthy         bool   `json:"healthy"`
	CooldownSeconds int    `json:"cooldown_seconds,omitempty"`
	Requests        int64  `json:"requests"`
	Errors          int64  `json:"errors"`
	LastStatus      int    `json:"last_status,omitempty"`
	LastError       string `json:"last_error,omitempty"`
}

// KeyRing manages zero or more API keys for a backend with thread-safe sticky or round-robin
// selection, per-key transient cooldown tracking, and per-key request telemetry.
type KeyRing struct {
	mu        sync.Mutex
	keys      []string
	idx       int
	rrCursor  int
	noKey     bool
	strategy  string
	coolUntil map[string]time.Time
	metrics   map[string]*keyMetric
	nowFunc   func() time.Time
}

// NormalizeKeyStrategy normalizes user/env key selection strategy names.
func NormalizeKeyStrategy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "round-robin", "round_robin", "roundrobin", "rr":
		return KeyStrategyRoundRobin
	default:
		return KeyStrategySticky
	}
}

// NewKeyRing creates a sticky KeyRing initialized with keys (deduplicated, preserving order).
func NewKeyRing(noKey bool, keys ...string) *KeyRing {
	return NewKeyRingWithStrategy(noKey, KeyStrategySticky, keys...)
}

// NewKeyRingWithStrategy creates a KeyRing using the specified key selection strategy.
func NewKeyRingWithStrategy(noKey bool, strategy string, keys ...string) *KeyRing {
	kr := &KeyRing{
		noKey:     noKey,
		strategy:  NormalizeKeyStrategy(strategy),
		coolUntil: make(map[string]time.Time),
		metrics:   make(map[string]*keyMetric),
		nowFunc:   time.Now,
	}
	kr.SetKeys(keys)
	return kr
}

func (kr *KeyRing) now() time.Time {
	if kr != nil && kr.nowFunc != nil {
		return kr.nowFunc()
	}
	return time.Now()
}

// Strategy returns the configured key selection mode ("sticky" or "round-robin").
func (kr *KeyRing) Strategy() string {
	if kr == nil {
		return KeyStrategySticky
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()
	if kr.strategy == "" {
		return KeyStrategySticky
	}
	return kr.strategy
}

// SetStrategy updates the key selection mode ("sticky" or "round-robin").
func (kr *KeyRing) SetStrategy(strategy string) {
	if kr == nil {
		return
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()
	kr.strategy = NormalizeKeyStrategy(strategy)
}

// SetKeys replaces the managed key list, pruning blanks and duplicates while preserving order.
func (kr *KeyRing) SetKeys(keys []string) {
	seen := make(map[string]bool, len(keys))
	clean := make([]string, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		clean = append(clean, k)
	}

	kr.mu.Lock()
	defer kr.mu.Unlock()
	kr.keys = clean
	kr.coolUntil = make(map[string]time.Time)
	kr.metrics = make(map[string]*keyMetric, len(clean))
	if kr.idx >= len(kr.keys) {
		kr.idx = 0
	}
	if kr.rrCursor >= len(kr.keys) {
		kr.rrCursor = 0
	}
}

// AddKeys appends additional keys without removing existing ones.
func (kr *KeyRing) AddKeys(keys ...string) {
	kr.mu.Lock()
	defer kr.mu.Unlock()

	seen := make(map[string]bool, len(kr.keys)+len(keys))
	for _, k := range kr.keys {
		seen[k] = true
	}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		kr.keys = append(kr.keys, k)
	}
}

// RemoveByMasked deletes the first key whose masked representation matches maskedLabel.
func (kr *KeyRing) RemoveByMasked(maskedLabel string) bool {
	if kr == nil || maskedLabel == "" {
		return false
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()
	for i, k := range kr.keys {
		if maskSecret(k) == maskedLabel {
			kr.keys = append(kr.keys[:i], kr.keys[i+1:]...)
			delete(kr.coolUntil, k)
			delete(kr.metrics, k)
			if len(kr.keys) == 0 {
				kr.idx = 0
				kr.rrCursor = 0
			} else {
				kr.idx = kr.idx % len(kr.keys)
				kr.rrCursor = kr.rrCursor % len(kr.keys)
			}
			return true
		}
	}
	return false
}

// FindByMasked returns the raw key whose masked label equals maskedLabel.
func (kr *KeyRing) FindByMasked(maskedLabel string) (string, bool) {
	if kr == nil || maskedLabel == "" {
		return "", false
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()
	for _, k := range kr.keys {
		if maskSecret(k) == maskedLabel {
			return k, true
		}
	}
	return "", false
}

// RemoveMatching drops every key for which match returns true and reports how many were removed.
func (kr *KeyRing) RemoveMatching(match func(key string) bool) int {
	if kr == nil || match == nil {
		return 0
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()
	kept := kr.keys[:0]
	removed := 0
	for _, k := range kr.keys {
		if match(k) {
			delete(kr.coolUntil, k)
			delete(kr.metrics, k)
			removed++
			continue
		}
		kept = append(kept, k)
	}
	kr.keys = kept
	if len(kr.keys) == 0 {
		kr.idx, kr.rrCursor = 0, 0
	} else {
		kr.idx %= len(kr.keys)
		kr.rrCursor %= len(kr.keys)
	}
	return removed
}

// ClearCooldowns clears transient per-key cooldown state across all keys in the ring.
func (kr *KeyRing) ClearCooldowns() {
	if kr == nil {
		return
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()
	kr.coolUntil = make(map[string]time.Time)
}

func (kr *KeyRing) isHealthyLocked(key string, now time.Time) bool {
	if kr.coolUntil == nil {
		return true
	}
	until, ok := kr.coolUntil[key]
	if !ok {
		return true
	}
	if !now.Before(until) {
		delete(kr.coolUntil, key)
		return true
	}
	return false
}

func (kr *KeyRing) metricForLocked(key string) *keyMetric {
	if kr.metrics == nil {
		kr.metrics = make(map[string]*keyMetric)
	}
	m := kr.metrics[key]
	if m == nil {
		m = &keyMetric{}
		kr.metrics[key] = m
	}
	return m
}

// Current returns the currently active healthy key without advancing the round-robin cursor.
func (kr *KeyRing) Current() string {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	n := len(kr.keys)
	if n == 0 {
		return ""
	}
	now := kr.now()
	cur := kr.keys[kr.idx%n]
	if kr.isHealthyLocked(cur, now) {
		return cur
	}
	for step := 1; step < n; step++ {
		candIdx := (kr.idx + step) % n
		if kr.isHealthyLocked(kr.keys[candIdx], now) {
			kr.idx = candIdx
			return kr.keys[candIdx]
		}
	}
	return cur
}

// Acquire picks the next healthy key for a new request according to the configured KeyStrategy
// ("round-robin" advances per call across healthy keys; "sticky" reuses the active healthy key).
func (kr *KeyRing) Acquire() string {
	if kr == nil {
		return ""
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()
	n := len(kr.keys)
	if n == 0 {
		return ""
	}
	now := kr.now()
	if kr.strategy == KeyStrategyRoundRobin && n > 1 {
		start := kr.rrCursor % n
		for step := 0; step < n; step++ {
			candIdx := (start + step) % n
			if kr.isHealthyLocked(kr.keys[candIdx], now) {
				kr.idx = candIdx
				kr.rrCursor = (candIdx + 1) % n
				return kr.keys[candIdx]
			}
		}
		// All keys are cooling down: advance rrCursor uniformly so callers do not pile onto slot 0.
		kr.idx = start
		kr.rrCursor = (start + 1) % n
		return kr.keys[start]
	}

	cur := kr.keys[kr.idx%n]
	if kr.isHealthyLocked(cur, now) {
		return cur
	}
	for step := 1; step < n; step++ {
		candIdx := (kr.idx + step) % n
		if kr.isHealthyLocked(kr.keys[candIdx], now) {
			kr.idx = candIdx
			return kr.keys[candIdx]
		}
	}
	return cur
}

// Rotate advances to the next healthy key if failedKey matches the active key and another key exists.
func (kr *KeyRing) Rotate(failedKey string) (next string, rotated bool) {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	n := len(kr.keys)
	if n <= 1 {
		if n == 1 {
			return kr.keys[0], false
		}
		return "", false
	}
	now := kr.now()
	cur := kr.keys[kr.idx%n]
	if failedKey != "" && failedKey != cur && kr.isHealthyLocked(cur, now) {
		return cur, false
	}
	for step := 1; step < n; step++ {
		candIdx := (kr.idx + step) % n
		if kr.isHealthyLocked(kr.keys[candIdx], now) {
			kr.idx = candIdx
			kr.rrCursor = (candIdx + 1) % n
			return kr.keys[candIdx], true
		}
	}
	kr.idx = (kr.idx + 1) % n
	kr.rrCursor = (kr.idx + 1) % n
	return kr.keys[kr.idx], true
}

// NextHealthyExcluding selects another healthy key not in tried, safe for concurrent round-robin relay.
func (kr *KeyRing) NextHealthyExcluding(tried map[string]bool) (string, bool) {
	if kr == nil {
		return "", false
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()
	n := len(kr.keys)
	if n <= 1 {
		return "", false
	}
	now := kr.now()
	start := (kr.idx + 1) % n
	for step := 0; step < n; step++ {
		candIdx := (start + step) % n
		candKey := kr.keys[candIdx]
		if tried != nil && tried[candKey] {
			continue
		}
		if kr.isHealthyLocked(candKey, now) {
			kr.idx = candIdx
			kr.rrCursor = (candIdx + 1) % n
			return candKey, true
		}
	}
	return "", false
}

// MarkFailure places failedKey in temporary key-level cooldown based on HTTP failure status.
func (kr *KeyRing) MarkFailure(failedKey string, status int) {
	kr.MarkFailureDetailed(failedKey, status, "", 0)
}

// MarkFailureWithDuration places failedKey in temporary key-level cooldown, honoring customDur
// when positive (e.g., from an upstream Retry-After header).
func (kr *KeyRing) MarkFailureWithDuration(failedKey string, status int, customDur time.Duration) {
	kr.MarkFailureDetailed(failedKey, status, "", customDur)
}

// MarkFailureDetailed records failure telemetry for failedKey and places it in cooldown.
func (kr *KeyRing) MarkFailureDetailed(failedKey string, status int, errMsg string, customDur time.Duration) {
	if kr == nil || failedKey == "" || kr.noKey {
		return
	}
	dur := customDur
	if dur <= 0 {
		dur = keyCooldownDuration(status)
	}
	now := kr.now()
	kr.mu.Lock()
	defer kr.mu.Unlock()

	m := kr.metricForLocked(failedKey)
	m.requests++
	m.errors++
	m.lastStatus = status
	if errMsg != "" {
		if len(errMsg) > 120 {
			errMsg = errMsg[:120] + "..."
		}
		m.lastError = errMsg
	}
	m.lastUsedAt = now

	if dur > 0 {
		if kr.coolUntil == nil {
			kr.coolUntil = make(map[string]time.Time)
		}
		kr.coolUntil[failedKey] = now.Add(dur)
	}
}

// MarkExhaustedAndRotate marks usedKey as temporarily cooled (e.g., x-ratelimit-remaining-requests: 0)
// and rotates the active index to the next healthy key when multiple keys are configured.
func (kr *KeyRing) MarkExhaustedAndRotate(usedKey string, resetAfter time.Duration) (string, bool) {
	if kr == nil || usedKey == "" || kr.noKey {
		return "", false
	}
	if resetAfter <= 0 {
		resetAfter = 15 * time.Second
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()

	now := kr.now()
	m := kr.metricForLocked(usedKey)
	m.requests++
	m.lastStatus = 200
	m.lastError = ""
	m.lastUsedAt = now

	n := len(kr.keys)
	if n <= 1 {
		return usedKey, false
	}
	if kr.coolUntil == nil {
		kr.coolUntil = make(map[string]time.Time)
	}
	kr.coolUntil[usedKey] = now.Add(resetAfter)

	cur := kr.keys[kr.idx%n]
	if cur != usedKey && kr.isHealthyLocked(cur, now) {
		return cur, false
	}
	for step := 1; step < n; step++ {
		candIdx := (kr.idx + step) % n
		if kr.isHealthyLocked(kr.keys[candIdx], now) {
			kr.idx = candIdx
			kr.rrCursor = (candIdx + 1) % n
			return kr.keys[candIdx], true
		}
	}
	kr.idx = (kr.idx + 1) % n
	kr.rrCursor = (kr.idx + 1) % n
	return kr.keys[kr.idx], true
}

// MarkSuccess clears any temporary cooldown for key and records a successful attempt.
func (kr *KeyRing) MarkSuccess(key string) {
	if kr == nil || key == "" {
		return
	}
	now := kr.now()
	kr.mu.Lock()
	defer kr.mu.Unlock()
	delete(kr.coolUntil, key)
	m := kr.metricForLocked(key)
	m.requests++
	m.lastStatus = 200
	m.lastError = ""
	m.lastUsedAt = now
}

// HealthyCount returns how many configured keys are not currently cooling down.
func (kr *KeyRing) HealthyCount() int {
	if kr == nil {
		return 0
	}
	if kr.noKey {
		return 1
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()
	now := kr.now()
	count := 0
	for _, k := range kr.keys {
		if kr.isHealthyLocked(k, now) {
			count++
		}
	}
	return count
}

func keyCooldownDuration(status int) time.Duration {
	switch {
	case status == 401 || status == 402 || status == 403:
		return 10 * time.Minute
	case status == 429:
		return 60 * time.Second
	case status == 503 || status == 529:
		return 15 * time.Second
	case status == 408 || status >= 500:
		return 30 * time.Second
	default:
		return 0
	}
}

// Count returns the number of configured keys.
func (kr *KeyRing) Count() int {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	return len(kr.keys)
}

// IsNoKey returns true for keyless backends such as local Ollama.
func (kr *KeyRing) IsNoKey() bool {
	return kr.noKey
}

// MaskedKeys returns a redacted representation of all configured keys safe for API responses.
func (kr *KeyRing) MaskedKeys() []string {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	out := make([]string, len(kr.keys))
	for i, k := range kr.keys {
		out[i] = maskSecret(k)
	}
	return out
}

// StatusSnapshot returns per-key health, cooldown remaining seconds, and usage telemetry.
func (kr *KeyRing) StatusSnapshot() []KeyStatus {
	if kr == nil {
		return nil
	}
	kr.mu.Lock()
	defer kr.mu.Unlock()

	n := len(kr.keys)
	if n == 0 {
		return nil
	}
	now := kr.now()
	activeIdx := kr.idx % n
	out := make([]KeyStatus, n)
	for i, k := range kr.keys {
		st := KeyStatus{
			Index:   i,
			Masked:  maskSecret(k),
			Active:  i == activeIdx,
			Healthy: true,
		}
		if until, ok := kr.coolUntil[k]; ok {
			if rem := until.Sub(now); rem > 0 {
				st.Healthy = false
				st.CooldownSeconds = int(math.Ceil(rem.Seconds()))
			} else {
				delete(kr.coolUntil, k)
			}
		}
		if m := kr.metrics[k]; m != nil {
			st.Requests = m.requests
			st.Errors = m.errors
			st.LastStatus = m.lastStatus
			st.LastError = m.lastError
		}
		out[i] = st
	}
	return out
}

func maskSecret(k string) string {
	n := len(k)
	if n <= 8 {
		return "****"
	}
	return k[:4] + "..." + k[n-4:]
}
