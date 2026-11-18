package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

// ModelInfo describes a model discovered from or configured on a provider.
type ModelInfo struct {
	ID         string `json:"id"`
	ProviderID string `json:"provider_id"`
	OwnedBy    string `json:"owned_by,omitempty"`
	Static     bool   `json:"static,omitempty"`
}

type catalogEntry struct {
	models      []ModelInfo
	embedModels []ModelInfo // embedding-capable models kept apart from chat routing
	fetchedAt   time.Time
	lastErr     string
}

// Catalog caches per-provider discovered model lists with configurable TTL.
type Catalog struct {
	mu           sync.RWMutex
	client       *http.Client
	ttl          time.Duration
	entries      map[string]*catalogEntry
	staticModels map[string][]string
	refreshLocks map[string]*sync.Mutex
}

// NewCatalog creates a model catalog backed by httpClient with a 10-minute default TTL.
func NewCatalog(client *http.Client) *Catalog {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Catalog{
		client:       client,
		ttl:          10 * time.Minute,
		entries:      make(map[string]*catalogEntry),
		staticModels: make(map[string][]string),
		refreshLocks: make(map[string]*sync.Mutex),
	}
}

func (c *Catalog) providerLock(providerID string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshLocks == nil {
		c.refreshLocks = make(map[string]*sync.Mutex)
	}
	l := c.refreshLocks[providerID]
	if l == nil {
		l = &sync.Mutex{}
		c.refreshLocks[providerID] = l
	}
	return l
}

// SetStaticModels registers static fallback model lists declared in config.
func (c *Catalog) SetStaticModels(providers []*Provider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.staticModels = make(map[string][]string, len(providers))
	for _, p := range providers {
		if len(p.StaticModels) > 0 {
			c.staticModels[p.ID] = append([]string(nil), p.StaticModels...)
		}
	}
}

// Snapshot returns cached + static models for providerID without triggering network IO.
func (c *Catalog) Snapshot(providerID string) (models []ModelInfo, fetchedAt time.Time, lastErr string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ent := c.entries[providerID]
	if ent != nil {
		models = append(models, ent.models...)
		fetchedAt = ent.fetchedAt
		lastErr = ent.lastErr
	}
	return mergeModelInfoLists(models, c.staticInfoLocked(providerID)), fetchedAt, lastErr
}

// EnsureFresh returns cached models for provider, refreshing synchronously if stale or uncached.
func (c *Catalog) EnsureFresh(ctx context.Context, p *Provider) []ModelInfo {
	if p == nil || !p.Available() {
		return nil
	}
	c.mu.RLock()
	ent := c.entries[p.ID]
	fresh := ent != nil && time.Since(ent.fetchedAt) < c.ttl
	c.mu.RUnlock()
	if fresh {
		models, _, _ := c.Snapshot(p.ID)
		return models
	}

	// Singleflight guard per provider prevents thundering-herd `/models` refreshes on cold start.
	plock := c.providerLock(p.ID)
	plock.Lock()
	defer plock.Unlock()

	c.mu.RLock()
	ent = c.entries[p.ID]
	fresh = ent != nil && time.Since(ent.fetchedAt) < c.ttl
	c.mu.RUnlock()
	if fresh {
		models, _, _ := c.Snapshot(p.ID)
		return models
	}
	models, _ := c.Refresh(ctx, p)
	return models
}

// Refresh forces a live upstream `/models` discovery request for provider p, rotating across keys on auth/rate errors.
func (c *Catalog) Refresh(ctx context.Context, p *Provider) ([]ModelInfo, error) {
	if p == nil {
		return nil, nil
	}
	urlStr, proto := p.ModelsListURL()
	if urlStr == "" {
		c.mu.RLock()
		staticList := c.staticInfoLocked(p.ID)
		c.mu.RUnlock()
		return staticList, nil
	}

	maxKeys := 1
	if p.Keys != nil && p.Keys.Count() > 1 {
		maxKeys = p.Keys.Count()
	}

	tried := make(map[string]bool, maxKeys)
	var key string
	if p.Keys != nil {
		key = p.Keys.Acquire()
	}

	var lastErr error
	for keyTry := 0; keyTry < maxKeys; keyTry++ {
		if key != "" {
			tried[key] = true
		}
		reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, urlStr, nil)
		if err != nil {
			cancel()
			return c.recordError(p.ID, err), err
		}
		p.ApplyHeaders(req, proto, key)

		resp, err := c.client.Do(req)
		if err != nil {
			cancel()
			return c.recordError(p.ID, err), err
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
		resp.Body.Close()
		cancel()
		if err != nil {
			return c.recordError(p.ID, err), err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			ge := parseHTTPStatusError(resp.StatusCode, body)
			lastErr = ge
			if shouldRotateCatalogKey(resp.StatusCode, body) && p.Keys != nil {
				dur := parseRetryAfterHeader(resp.Header)
				if resp.StatusCode == 400 && dur == 0 {
					dur = time.Hour
				}
				p.Keys.MarkFailureDetailed(key, resp.StatusCode, ge.Error(), dur)
				if keyTry+1 < maxKeys {
					if nextKey, ok := p.Keys.NextHealthyExcluding(tried); ok {
						key = nextKey
						continue
					}
				}
			}
			return c.recordError(p.ID, ge), ge
		}

		if p.Keys != nil {
			p.Keys.MarkSuccess(key)
		}
		parsed := parseModelsResponse(p.ID, proto, p.IsFreeOnly(), p.IsIncludeNonChat(), body)
		c.mu.Lock()
		c.entries[p.ID] = &catalogEntry{
			models:      parsed,
			embedModels: parseEmbeddingModels(p.ID, proto, p.IsFreeOnly(), body),
			fetchedAt:   time.Now(),
		}
		merged := mergeModelInfoLists(parsed, c.staticInfoLocked(p.ID))
		c.mu.Unlock()
		return merged, nil
	}

	return c.recordError(p.ID, lastErr), lastErr
}

// HasExactModel reports whether p advertises modelID (case-exact, checked against cache or refreshed once).
func (c *Catalog) HasExactModel(ctx context.Context, p *Provider, modelID string) bool {
	if p == nil || !p.Available() {
		return false
	}
	models := c.EnsureFresh(ctx, p)
	for _, m := range models {
		if m.ID == modelID {
			return true
		}
	}
	return false
}

// MatchCanonicalModel returns the provider's native model ID whose CanonicalModelID equals
// targetCanon (preferring exact/case-insensitive matches over suffixed variants).
func (c *Catalog) MatchCanonicalModel(ctx context.Context, p *Provider, clientModel string) (upstreamID string, ok bool) {
	if p == nil || !p.Available() {
		return "", false
	}
	targetCanon := CanonicalModelID(clientModel)
	if targetCanon == "" {
		return "", false
	}
	return matchCanonicalInList(c.EnsureFresh(ctx, p), clientModel, targetCanon)
}

// SnapshotCanonicalMatch returns the native model ID matching targetCanon from cached models without I/O.
func (c *Catalog) SnapshotCanonicalMatch(providerID, clientModel string) (upstreamID string, ok bool) {
	targetCanon := CanonicalModelID(clientModel)
	if targetCanon == "" {
		return "", false
	}
	models, _, _ := c.Snapshot(providerID)
	return matchCanonicalInList(models, clientModel, targetCanon)
}

func matchCanonicalInList(models []ModelInfo, clientModel, targetCanon string) (string, bool) {
	for _, m := range models {
		if m.ID == clientModel {
			return m.ID, true
		}
	}
	for _, m := range models {
		if strings.EqualFold(m.ID, clientModel) {
			return m.ID, true
		}
	}
	var freeCand string
	for _, m := range models {
		if CanonicalModelID(m.ID) == targetCanon {
			if strings.HasSuffix(strings.ToLower(m.ID), ":free") {
				if freeCand == "" {
					freeCand = m.ID
				}
				continue
			}
			return m.ID, true
		}
	}
	if freeCand != "" {
		return freeCand, true
	}
	return "", false
}

func shouldRotateCatalogKey(status int, body []byte) bool {
	if status == 401 || status == 402 || status == 403 || status == 429 || status == 503 || status == 529 {
		return true
	}
	if status == 400 {
		lower := strings.ToLower(string(body))
		return strings.Contains(lower, "api_key_invalid") || strings.Contains(lower, "api key not valid")
	}
	return false
}

func (c *Catalog) recordError(providerID string, err error) []ModelInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	ent, ok := c.entries[providerID]
	if !ok {
		ent = &catalogEntry{}
		c.entries[providerID] = ent
	}
	ent.lastErr = err.Error()
	// Avoid hot-looping failed refreshes on every incoming request; retry after 60s.
	ent.fetchedAt = time.Now().Add(-(c.ttl - time.Minute))
	return mergeModelInfoLists(ent.models, c.staticInfoLocked(providerID))
}

func (c *Catalog) staticInfoLocked(providerID string) []ModelInfo {
	raw := c.staticModels[providerID]
	out := make([]ModelInfo, 0, len(raw))
	for _, id := range raw {
		out = append(out, ModelInfo{
			ID:         id,
			ProviderID: providerID,
			OwnedBy:    providerID,
			Static:     true,
		})
	}
	return out
}

type openAIModelCatalogItem struct {
	ID           string         `json:"id"`
	OwnedBy      string         `json:"owned_by"`
	Pricing      map[string]any `json:"pricing,omitempty"`
	Architecture *struct {
		OutputModalities []string `json:"output_modalities,omitempty"`
	} `json:"architecture,omitempty"`
}

func parseModelsResponse(providerID string, proto ir.Protocol, freeOnly, includeNonChat bool, body []byte) []ModelInfo {
	if proto == ir.ProtoGemini {
		var gemResp struct {
			Models []struct {
				Name                       string   `json:"name"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"`
			} `json:"models"`
		}
		if json.Unmarshal(body, &gemResp) == nil && len(gemResp.Models) > 0 {
			out := make([]ModelInfo, 0, len(gemResp.Models))
			for _, m := range gemResp.Models {
				id := strings.TrimPrefix(strings.TrimSpace(m.Name), "models/")
				if id == "" {
					continue
				}
				if !includeNonChat {
					if !IsChatCapableModel(providerID, id) {
						continue
					}
					if len(m.SupportedGenerationMethods) > 0 && !supportsGenerateContent(m.SupportedGenerationMethods) {
						continue
					}
				}
				out = append(out, ModelInfo{
					ID:         id,
					ProviderID: providerID,
					OwnedBy:    "google",
				})
			}
			return out
		}
	}

	var oaiResp struct {
		Data   []openAIModelCatalogItem `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &oaiResp) != nil {
		return nil
	}
	out := make([]ModelInfo, 0, len(oaiResp.Data)+len(oaiResp.Models))
	for _, m := range oaiResp.Data {
		if m.ID == "" {
			continue
		}
		if !includeNonChat && !IsChatCapableModel(providerID, m.ID) {
			continue
		}
		if freeOnly && !isCatalogItemFree(m) {
			continue
		}
		owned := m.OwnedBy
		if owned == "" {
			owned = providerID
		}
		out = append(out, ModelInfo{
			ID:         m.ID,
			ProviderID: providerID,
			OwnedBy:    owned,
		})
	}
	for _, m := range oaiResp.Models {
		id := m.ID
		if id == "" {
			id = m.Name
		}
		id = strings.TrimPrefix(strings.TrimSpace(id), "models/")
		if id == "" {
			continue
		}
		if !includeNonChat && !IsChatCapableModel(providerID, id) {
			continue
		}
		if freeOnly && !hasFreeSuffixOrRouter(id) {
			continue
		}
		out = append(out, ModelInfo{
			ID:         id,
			ProviderID: providerID,
			OwnedBy:    providerID,
		})
	}
	return out
}

// IsChatCapableModel filters out pure TTS, transcription, image generation, embedding,
// moderation, and AMD systemone endpoints from gateway chat/responses/messages catalogs.
func IsChatCapableModel(providerID, modelID string) bool {
	lower := strings.ToLower(strings.TrimSpace(modelID))
	if lower == "" {
		return false
	}

	if providerID == "amd" {
		switch lower {
		case "clef", "d3", "d3-flash":
			return false
		}
	}

	// Shared non-chat capability markers across OpenAI, NVIDIA, Gemini, Ollama, and OpenRouter.
	for _, substr := range []string{
		"text-embedding",
		"-embedding",
		"embedding-",
		"nv-embed",
		"nemoretriever",
		"rerank",
		"moderation",
		"-tts",
		"tts-",
		"whisper-",
		"dall-e-",
		"gpt-image-",
		"chatgpt-image-",
		"imagen-",
		"veo-",
		"sora-",
		"lyria-",
		"gpt-realtime",
		"gpt-audio",
		"-audio-",
		"-realtime-",
	} {
		if strings.Contains(lower, substr) {
			return false
		}
	}
	switch lower {
	case "tts-1", "tts-1-hd", "babbage-002", "davinci-002", "aqa":
		return false
	}
	if strings.HasSuffix(lower, "-embed") || strings.HasSuffix(lower, "-retriever") {
		return false
	}
	// Catch-all for embedding models such as nvidia/nemotron-3.5-embed-8b or snowflake/arctic-embed-l,
	// which are served via /v1/embeddings instead of chat routing.
	return !IsEmbeddingModel(lower)
}

func supportsGenerateContent(methods []string) bool {
	for _, m := range methods {
		if strings.EqualFold(strings.TrimSpace(m), "generateContent") {
			return true
		}
	}
	return false
}

func parseRetryAfterHeader(h http.Header) time.Duration {
	raw := strings.TrimSpace(h.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		d := time.Duration(secs) * time.Second
		if d > 15*time.Minute {
			return 15 * time.Minute
		}
		return d
	}
	if t, err := http.ParseTime(raw); err == nil {
		if d := time.Until(t); d > 0 {
			if d > 15*time.Minute {
				return 15 * time.Minute
			}
			return d
		}
	}
	return 0
}

// isCatalogItemFree filters catalog entries (e.g., OpenRouter /models) to zero-cost text models.
func isCatalogItemFree(m openAIModelCatalogItem) bool {
	if m.Architecture != nil && len(m.Architecture.OutputModalities) > 0 {
		hasText := false
		for _, mod := range m.Architecture.OutputModalities {
			if strings.EqualFold(strings.TrimSpace(mod), "text") {
				hasText = true
				break
			}
		}
		if !hasText {
			return false
		}
	}

	if hasFreeSuffixOrRouter(m.ID) && len(m.Pricing) == 0 {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(m.ID), "openrouter/free") {
		return true
	}
	if len(m.Pricing) == 0 {
		return false
	}

	// OpenRouter signals free models with "0" prompt and completion prices.
	promptPrice, hasPrompt := parsePricingFloat(m.Pricing["prompt"])
	compPrice, hasComp := parsePricingFloat(m.Pricing["completion"])
	if !hasPrompt || !hasComp || promptPrice != 0 || compPrice != 0 {
		return false
	}
	for _, extraKey := range []string{"request", "internal_reasoning", "image", "image_output"} {
		raw, exists := m.Pricing[extraKey]
		if !exists || raw == nil {
			continue
		}
		if price, ok := parsePricingFloat(raw); ok && price != 0 {
			return false
		}
	}
	return true
}

func hasFreeSuffixOrRouter(id string) bool {
	lower := strings.ToLower(strings.TrimSpace(id))
	return lower == "openrouter/free" || strings.HasSuffix(lower, ":free")
}

func parsePricingFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func mergeModelInfoLists(primary, fallback []ModelInfo) []ModelInfo {
	seen := make(map[string]bool, len(primary)+len(fallback))
	out := make([]ModelInfo, 0, len(primary)+len(fallback))
	for _, m := range primary {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	for _, m := range fallback {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	return out
}

func parseHTTPStatusError(status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 256 {
		msg = msg[:256] + "..."
	}
	return ir.NewError(status, "catalog_fetch_failed", msg)
}
