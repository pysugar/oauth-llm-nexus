// Package router resolves logical models to ordered backend candidates, failover plans, and cooldown state.
package router

import (
	"sync"
	"time"
)

// CooldownInfo describes the current cooldown state of a provider.
type CooldownInfo struct {
	CoolingDown bool      `json:"cooling_down"`
	Until       time.Time `json:"until,omitempty"`
	RemainingS  int       `json:"remaining_seconds,omitempty"`
	Failures    int       `json:"failures"`
	Reason      string    `json:"reason,omitempty"`
}

type cooldownEntry struct {
	until    time.Time
	failures int
	reason   string
}

// Cooldown tracks per-provider transient backoff windows after upstream errors.
type Cooldown struct {
	mu      sync.RWMutex
	entries map[string]*cooldownEntry
	nowFunc func() time.Time
}

// NewCooldown initializes a thread-safe provider cooldown tracker.
func NewCooldown() *Cooldown {
	return &Cooldown{
		entries: make(map[string]*cooldownEntry),
		nowFunc: time.Now,
	}
}

func (c *Cooldown) now() time.Time {
	if c != nil && c.nowFunc != nil {
		return c.nowFunc()
	}
	return time.Now()
}

// IsCoolingDown reports whether providerID is currently within an active cooldown window.
func (c *Cooldown) IsCoolingDown(providerID string) (bool, time.Duration, string) {
	if c == nil {
		return false, 0, ""
	}
	c.mu.RLock()
	ent := c.entries[providerID]
	c.mu.RUnlock()
	if ent == nil {
		return false, 0, ""
	}
	rem := ent.until.Sub(c.now())
	if rem <= 0 {
		return false, 0, ""
	}
	return true, rem, ent.reason
}

// MarkFailure places providerID in exponential backoff based on failure class and consecutive count.
func (c *Cooldown) MarkFailure(providerID string, status int, reason string) time.Duration {
	return c.MarkFailureWithRetry(providerID, status, reason, 0)
}

// MarkFailureWithRetry places providerID in cooldown, preferring retryAfter when positive
// (parsed from upstream Retry-After / x-ratelimit-reset headers) over exponential backoff.
func (c *Cooldown) MarkFailureWithRetry(providerID string, status int, reason string, retryAfter time.Duration) time.Duration {
	if c == nil || providerID == "" {
		return 0
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()

	ent, exists := c.entries[providerID]
	if !exists {
		ent = &cooldownEntry{}
		c.entries[providerID] = ent
	}
	ent.failures++
	ent.reason = reason

	dur := retryAfter
	if dur <= 0 {
		dur = computeBackoff(status, ent.failures)
	}
	ent.until = now.Add(dur)
	return dur
}

// RecordSuccess clears cooldown and failure counters for providerID.
func (c *Cooldown) RecordSuccess(providerID string) {
	if c == nil || providerID == "" {
		return
	}
	c.mu.Lock()
	delete(c.entries, providerID)
	c.mu.Unlock()
}

// Clear removes cooldown state for providerID, or all providers when providerID is empty.
func (c *Cooldown) Clear(providerID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if providerID == "" {
		c.entries = make(map[string]*cooldownEntry)
		return
	}
	delete(c.entries, providerID)
}

// Snapshot returns active cooldown metadata keyed by provider ID.
func (c *Cooldown) Snapshot() map[string]CooldownInfo {
	if c == nil {
		return nil
	}
	now := c.now()
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make(map[string]CooldownInfo, len(c.entries))
	for id, ent := range c.entries {
		rem := ent.until.Sub(now)
		if rem <= 0 {
			continue
		}
		out[id] = CooldownInfo{
			CoolingDown: true,
			Until:       ent.until,
			RemainingS:  int(rem.Seconds()),
			Failures:    ent.failures,
			Reason:      ent.reason,
		}
	}
	return out
}

func computeBackoff(status int, failures int) time.Duration {
	var base, max time.Duration
	switch {
	case status == 401 || status == 403:
		base = 2 * time.Minute
		max = 10 * time.Minute
	case status == 429:
		base = 30 * time.Second
		max = 5 * time.Minute
	default:
		base = 15 * time.Second
		max = 2 * time.Minute
	}
	shift := failures - 1
	if shift > 4 {
		shift = 4
	}
	d := base * time.Duration(1<<shift)
	if d > max {
		return max
	}
	return d
}
