package server

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/router"
)

// Affinity tuning: a conversation stays on the backend that answered it while that backend's
// prompt cache is worth keeping.
const (
	affinityMinCacheTokens = 1024
	affinityColdDefault    = 5 * time.Minute
	affinityColdLong       = time.Hour // vendors with long-lived caches (DeepSeek)
	affinityMaxEntries     = 4096
	affinityKeep           = 30 * 24 * time.Hour
	usageHalfLife          = time.Hour
)

type stick struct {
	provider    string
	cacheTokens int
	at          time.Time
}

type affinityStore struct {
	mu      sync.Mutex
	entries map[string]stick
}

func (a *affinityStore) get(key string) (stick, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.entries[key]
	if ok && time.Since(st.at) > affinityKeep {
		delete(a.entries, key)
		return stick{}, false
	}
	return st, ok
}

func (a *affinityStore) put(key string, st stick) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries == nil {
		a.entries = make(map[string]stick)
	}
	if len(a.entries) >= affinityMaxEntries {
		// Evict the oldest quarter.
		type kv struct {
			k  string
			at time.Time
		}
		all := make([]kv, 0, len(a.entries))
		for k, v := range a.entries {
			all = append(all, kv{k, v.at})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
		for _, e := range all[:len(all)/4] {
			delete(a.entries, e.k)
		}
	}
	a.entries[key] = st
}

// usageTracker keeps exponentially decayed token counts per provider (for StrategyUsage).
type usageTracker struct {
	mu   sync.Mutex
	vals map[string]decayed
}

type decayed struct {
	v  float64
	at time.Time
}

func decay(d decayed, now time.Time) float64 {
	if d.at.IsZero() {
		return 0
	}
	return d.v * math.Exp2(-now.Sub(d.at).Seconds()/usageHalfLife.Seconds())
}

func (u *usageTracker) add(provider string, tokens int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.vals == nil {
		u.vals = make(map[string]decayed)
	}
	now := time.Now()
	u.vals[provider] = decayed{v: decay(u.vals[provider], now) + float64(tokens), at: now}
}

func (u *usageTracker) score(provider string) float64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return decay(u.vals[provider], time.Now())
}

type routingState struct {
	affinity affinityStore
	usage    usageTracker
	rot      sync.Map // client model -> *atomic.Uint64
}

// orderPlan reorders a non-pinned plan's candidates: the model's strategy first, then capability
// filters (too-small context windows and non-vision models for image requests go to the back),
// then the conversation's sticky backend to the front, with cooling candidates always last.
func (s *Server) orderPlan(plan *router.Plan, req *ir.Request, session string) {
	if plan == nil || plan.Forced || plan.Strategy == models.StrategyPinned || len(plan.Candidates) < 2 {
		return
	}
	var avail, cooling []router.Candidate
	for _, c := range plan.Candidates {
		if c.Cooling {
			cooling = append(cooling, c)
		} else {
			avail = append(avail, c)
		}
	}

	switch plan.Strategy {
	case models.StrategyRotate:
		if len(avail) > 1 {
			v, _ := s.routing.rot.LoadOrStore(plan.ClientModel, new(atomic.Uint64))
			n := int(v.(*atomic.Uint64).Add(1)-1) % len(avail)
			avail = append(avail[n:], avail[:n]...)
		}
	case models.StrategyUsage:
		sort.SliceStable(avail, func(i, j int) bool {
			return s.routing.usage.score(avail[i].Provider.ID) < s.routing.usage.score(avail[j].Provider.ID)
		})
	case models.StrategyFastest:
		lat := func(c router.Candidate) int64 {
			if snap := s.Metrics.ProviderSnapshot(c.Provider.ID); snap != nil && snap.EWMALatencyMS > 0 {
				return snap.EWMALatencyMS
			}
			return math.MaxInt64 // unmeasured after measured
		}
		sort.SliceStable(avail, func(i, j int) bool { return lat(avail[i]) < lat(avail[j]) })
	}

	if req != nil && s.Meta != nil {
		est := estimateTokens(req)
		images := hasImages(req)
		rank := func(c router.Candidate) int {
			meta, ok := s.lookupMeta(c.Provider.ID, c.UpstreamModel)
			if !ok {
				return 1
			}
			r := 0
			if meta.Context > 0 && meta.Context < est {
				r += 4 // too small for this conversation: last resort
			}
			if images && meta.Vision != nil {
				if !*meta.Vision {
					r += 2
				}
			} else if images {
				r++
			}
			return r
		}
		sort.SliceStable(avail, func(i, j int) bool { return rank(avail[i]) < rank(avail[j]) })
	}

	if session != "" {
		if st, ok := s.routing.affinity.get(session + "|" + plan.ClientModel); ok && s.shouldStick(st, req) {
			for i, c := range avail {
				if c.Provider.ID == st.provider && i > 0 {
					avail = append([]router.Candidate{c}, append(avail[:i:i], avail[i+1:]...)...)
					break
				}
			}
		}
	}
	plan.Candidates = append(avail, cooling...)
}

func (s *Server) shouldStick(st stick, req *ir.Request) bool {
	if req != nil && len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == ir.RoleTool {
		return true // mid-turn (answering tool calls): always the same backend
	}
	if st.cacheTokens < affinityMinCacheTokens {
		return false
	}
	cold := affinityColdDefault
	if p, ok := s.Registry.Get(st.provider); ok && (p.Preset == "deepseek" || p.ID == "deepseek") {
		cold = affinityColdLong
	}
	return time.Since(st.at) <= cold
}

// observeRouting feeds affinity and the usage tracker after a successful call.
func (s *Server) observeRouting(session, clientModel, providerID string, u ir.Usage) {
	if providerID == "" {
		return
	}
	s.routing.usage.add(providerID, u.InputTokens+u.OutputTokens)
	if session != "" {
		s.routing.affinity.put(session+"|"+clientModel, stick{
			provider:    providerID,
			cacheTokens: u.CacheReadTokens + u.CacheWriteTokens,
			at:          time.Now(),
		})
	}
}

// estimateTokens approximates the prompt size: text bytes/4 plus ~1200 per image.
func estimateTokens(req *ir.Request) int {
	n := 0
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Result)
			for _, rp := range p.ResultParts {
				n += len(rp.Text)
				if rp.Kind == ir.KindImage {
					n += 4800
				}
			}
			if p.Kind == ir.KindImage {
				n += 4800
			}
		}
	}
	for _, t := range req.Tools {
		n += len(t.Description) + len(t.Schema)
	}
	return n / 4
}

func hasImages(req *ir.Request) bool {
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if p.Kind == ir.KindImage {
				return true
			}
			for _, rp := range p.ResultParts {
				if rp.Kind == ir.KindImage {
					return true
				}
			}
		}
	}
	return false
}
