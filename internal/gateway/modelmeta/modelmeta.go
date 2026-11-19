// Package modelmeta serves per-model pricing, context windows and modalities from models.dev,
// cached on disk next to the database, with user price overrides on top.
package modelmeta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
)

// DefaultURL is the models.dev catalogue.
const DefaultURL = "https://models.dev/api.json"

// Meta is what is known about one model. Prices are USD per 1M tokens.
type Meta struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	HasPrice   bool    `json:"has_price"`
	Context    int     `json:"context,omitempty"`
	MaxOutput  int     `json:"max_output,omitempty"`
	Vision     *bool   `json:"vision,omitempty"`
	Reasoning  bool    `json:"reasoning,omitempty"`
	Source     string  `json:"source"` // "models.dev:<provider>", "canonical:<provider>", "override", "local"
}

// Cost prices usage at m's rates. Cache reads/writes without their own price bill at input.
func (m Meta) Cost(u ir.Usage) float64 {
	if !m.HasPrice {
		return 0
	}
	cr, cw := m.CacheRead, m.CacheWrite
	if cr == 0 {
		cr = m.Input
	}
	if cw == 0 {
		cw = m.Input
	}
	return (float64(u.UncachedInputTokens())*m.Input +
		float64(u.CacheReadTokens)*cr +
		float64(u.CacheWriteTokens)*cw +
		float64(u.OutputTokens)*m.Output) / 1e6
}

// PriceOverride lets the user set a model's price; providerID "*" applies to every provider.
type PriceOverride func(providerID, model string) (Meta, bool)

// Options configures a Registry.
type Options struct {
	CachePath string
	URL       string
	Disabled  bool // never touch the network (NEXUS_MODELS_DEV=off)
	HTTP      *http.Client
	Now       func() time.Time
	Overrides PriceOverride
}

// Registry indexes models.dev and answers lookups. Safe for concurrent use.
type Registry struct {
	opts      Options
	mu        sync.RWMutex
	byProv    map[string]map[string]Meta
	canonical map[string]Meta
	fetchedAt time.Time
	lastErr   string
}

// OptionsFromEnv applies NEXUS_MODELS_DEV (off) and NEXUS_MODELS_DEV_URL.
func OptionsFromEnv(cachePath string) Options {
	o := Options{CachePath: cachePath, URL: DefaultURL}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NEXUS_MODELS_DEV"))) {
	case "off", "false", "0", "no":
		o.Disabled = true
	}
	if u := strings.TrimSpace(os.Getenv("NEXUS_MODELS_DEV_URL")); u != "" {
		o.URL = u
	}
	return o
}

// New loads the on-disk cache (if any); call Start to keep it fresh.
func New(opts Options) *Registry {
	if opts.URL == "" {
		opts.URL = DefaultURL
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	r := &Registry{opts: opts}
	if opts.CachePath != "" {
		if raw, err := os.ReadFile(opts.CachePath); err == nil {
			if err := r.load(raw); err == nil {
				if st, err := os.Stat(opts.CachePath); err == nil {
					r.fetchedAt = st.ModTime()
				}
			}
		}
	}
	return r
}

// Start refreshes the catalogue when it is older than 24h, checking hourly; failures back off
// from 1 minute up to 6 hours. It returns immediately.
func (r *Registry) Start(ctx context.Context) {
	if r == nil || r.opts.Disabled {
		return
	}
	go func() {
		backoff := time.Minute
		for {
			wait := time.Hour
			if r.stale() {
				if err := r.Refresh(ctx); err != nil {
					log.Printf("⚠️ models.dev refresh failed (retry in %s): %v", backoff, err)
					wait = backoff
					if backoff *= 2; backoff > 6*time.Hour {
						backoff = 6 * time.Hour
					}
				} else {
					backoff = time.Minute
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
}

func (r *Registry) stale() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byProv == nil || r.opts.Now().Sub(r.fetchedAt) > 24*time.Hour
}

// Refresh fetches the catalogue now and writes the cache file atomically.
func (r *Registry) Refresh(ctx context.Context) error {
	if r.opts.Disabled {
		return errors.New("models.dev disabled")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.opts.URL, nil)
	if err != nil {
		return err
	}
	resp, err := r.opts.HTTP.Do(req)
	if err != nil {
		r.setErr(err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("models.dev returned %d", resp.StatusCode)
		r.setErr(err)
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		r.setErr(err)
		return err
	}
	if err := r.load(raw); err != nil {
		r.setErr(err)
		return err
	}
	r.mu.Lock()
	r.fetchedAt = r.opts.Now()
	r.lastErr = ""
	r.mu.Unlock()
	if r.opts.CachePath != "" {
		tmp := r.opts.CachePath + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o644); err == nil {
			_ = os.Rename(tmp, r.opts.CachePath)
		}
	}
	return nil
}

func (r *Registry) setErr(err error) {
	r.mu.Lock()
	r.lastErr = err.Error()
	r.mu.Unlock()
}

// Status reports catalogue freshness for the admin API.
func (r *Registry) Status() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, m := range r.byProv {
		n += len(m)
	}
	out := map[string]any{"models": n, "disabled": r.opts.Disabled}
	if !r.fetchedAt.IsZero() {
		out["fetched_at"] = r.fetchedAt.UTC().Format(time.RFC3339)
	}
	if r.lastErr != "" {
		out["error"] = r.lastErr
	}
	return out
}

type mdModel struct {
	Cost *struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"cost"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Reasoning bool `json:"reasoning"`
}

// firstParty are vendors whose own listing wins for canonical (cross-provider) lookups.
var firstParty = []string{"openai", "anthropic", "google", "deepseek", "moonshotai", "mistral", "xai", "alibaba", "zhipuai", "minimax"}

func (r *Registry) load(raw []byte) error {
	var doc map[string]struct {
		Models map[string]mdModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	byProv := make(map[string]map[string]Meta, len(doc))
	for pid, p := range doc {
		ms := make(map[string]Meta, len(p.Models))
		for mid, m := range p.Models {
			ms[mid] = toMeta(m, "models.dev:"+pid)
		}
		byProv[pid] = ms
	}
	canonical := make(map[string]Meta)
	add := func(pid string) {
		for mid, m := range byProv[pid] {
			key := provider.CanonicalModelID(mid)
			if _, ok := canonical[key]; !ok && key != "" {
				m.Source = "canonical:" + pid
				canonical[key] = m
			}
		}
	}
	for _, pid := range firstParty {
		add(pid)
	}
	for pid := range byProv {
		add(pid)
	}
	r.mu.Lock()
	r.byProv, r.canonical = byProv, canonical
	r.mu.Unlock()
	return nil
}

func toMeta(m mdModel, source string) Meta {
	out := Meta{Context: m.Limit.Context, MaxOutput: m.Limit.Output, Reasoning: m.Reasoning, Source: source}
	if m.Cost != nil {
		out.HasPrice = true
		out.Input, out.Output = m.Cost.Input, m.Cost.Output
		out.CacheRead, out.CacheWrite = m.Cost.CacheRead, m.Cost.CacheWrite
	}
	if len(m.Modalities.Input) > 0 {
		v := false
		for _, mod := range m.Modalities.Input {
			if mod == "image" {
				v = true
			}
		}
		out.Vision = &v
	}
	return out
}

// presetSource maps gateway presets onto models.dev provider ids.
var presetSource = map[string]string{
	"openai": "openai", "openrouter": "openrouter", "nvidia": "nvidia", "deepseek": "deepseek",
	"gemini": "google", "amd": "amd", "anthropic": "anthropic", "voyage": "voyage",
}

// Lookup finds metadata for model as served by a provider of the given preset/id: user override,
// the provider's own models.dev listing, then the model's canonical listing.
func (r *Registry) Lookup(preset, providerID, model string) (Meta, bool) {
	if r == nil || model == "" {
		return Meta{}, false
	}
	if r.opts.Overrides != nil {
		if m, ok := r.opts.Overrides(providerID, model); ok {
			return m, true
		}
	}
	if preset == "ollama" || providerID == "ollama" {
		return Meta{HasPrice: true, Source: "local"}, true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	src := presetSource[preset]
	if src == "" {
		src = presetSource[providerID]
	}
	if ms := r.byProv[src]; ms != nil {
		bare := strings.TrimPrefix(model, "models/")
		if m, ok := ms[bare]; ok {
			return m, true
		}
	}
	if m, ok := r.canonical[provider.CanonicalModelID(model)]; ok {
		return m, true
	}
	return Meta{}, false
}

// CachePathNear returns the cache file path next to the SQLite database at dbPath.
func CachePathNear(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "models.dev.json")
}
