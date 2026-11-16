package server

import (
	"sort"
	"sync"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

// ProviderMetricSnapshot describes aggregated call and token telemetry for one backend provider.
type ProviderMetricSnapshot struct {
	ProviderID      string `json:"provider_id"`
	Requests        int64  `json:"requests"`
	Success         int64  `json:"success"`
	Errors          int64  `json:"errors"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	CachedTokens    int64  `json:"cached_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
	AvgLatencyMS    int64  `json:"avg_latency_ms"`
	EWMALatencyMS   int64  `json:"ewma_latency_ms"`
	LastStatus      int    `json:"last_status,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	LastUsedAt      string `json:"last_used_at,omitempty"`
}

// ModelMetricSnapshot describes aggregated telemetry for one logical client model.
type ModelMetricSnapshot struct {
	Model        string `json:"model"`
	Requests     int64  `json:"requests"`
	Success      int64  `json:"success"`
	Errors       int64  `json:"errors"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	AvgLatencyMS int64  `json:"avg_latency_ms"`
	LastProvider string `json:"last_provider,omitempty"`
	LastUsedAt   string `json:"last_used_at,omitempty"`
}

// MetricsSnapshot summarizes always-on gateway request, latency, and token metrics.
type MetricsSnapshot struct {
	StartedAt       string                   `json:"started_at"`
	Requests        int64                    `json:"requests"`
	Success         int64                    `json:"success"`
	Errors          int64                    `json:"errors"`
	InputTokens     int64                    `json:"input_tokens"`
	OutputTokens    int64                    `json:"output_tokens"`
	CachedTokens    int64                    `json:"cached_tokens"`
	ReasoningTokens int64                    `json:"reasoning_tokens"`
	Providers       []ProviderMetricSnapshot `json:"providers"`
	Models          []ModelMetricSnapshot    `json:"models"`
}

type providerMetricState struct {
	requests        int64
	success         int64
	errors          int64
	inputTokens     int64
	outputTokens    int64
	cachedTokens    int64
	reasoningTokens int64
	totalLatencyMS  int64
	ewmaLatencyMS   float64
	lastStatus      int
	lastError       string
	lastUsedAt      time.Time
}

type modelMetricState struct {
	requests       int64
	success        int64
	errors         int64
	inputTokens    int64
	outputTokens   int64
	totalLatencyMS int64
	lastProvider   string
	lastUsedAt     time.Time
}

// Metrics maintains always-on lightweight request, latency, and token counters in memory
// regardless of whether verbose RequestLog payload persistence is enabled.
type Metrics struct {
	mu              sync.RWMutex
	startedAt       time.Time
	requests        int64
	success         int64
	errors          int64
	inputTokens     int64
	outputTokens    int64
	cachedTokens    int64
	reasoningTokens int64
	byProvider      map[string]*providerMetricState
	byModel         map[string]*modelMetricState
}

// NewMetrics initializes an empty gateway telemetry collector.
func NewMetrics() *Metrics {
	return &Metrics{
		startedAt:  time.Now().UTC(),
		byProvider: make(map[string]*providerMetricState),
		byModel:    make(map[string]*modelMetricState),
	}
}

// RecordSuccess records a completed 2xx inference attempt along with latency and token usage.
func (m *Metrics) RecordSuccess(providerID, clientModel string, status int, latency time.Duration, usage ir.Usage) {
	if m == nil {
		return
	}
	ms := latency.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	now := time.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests++
	m.success++
	m.inputTokens += int64(usage.InputTokens)
	m.outputTokens += int64(usage.OutputTokens)
	m.cachedTokens += int64(usage.CacheReadTokens)
	m.reasoningTokens += int64(usage.ReasoningTokens)

	if providerID != "" {
		ps := m.ensureProviderLocked(providerID)
		ps.requests++
		ps.success++
		ps.inputTokens += int64(usage.InputTokens)
		ps.outputTokens += int64(usage.OutputTokens)
		ps.cachedTokens += int64(usage.CacheReadTokens)
		ps.reasoningTokens += int64(usage.ReasoningTokens)
		ps.totalLatencyMS += ms
		if ps.requests == 1 {
			ps.ewmaLatencyMS = float64(ms)
		} else {
			ps.ewmaLatencyMS = ps.ewmaLatencyMS*0.8 + float64(ms)*0.2
		}
		ps.lastStatus = status
		ps.lastError = ""
		ps.lastUsedAt = now
	}

	if clientModel != "" {
		msState := m.ensureModelLocked(clientModel)
		msState.requests++
		msState.success++
		msState.inputTokens += int64(usage.InputTokens)
		msState.outputTokens += int64(usage.OutputTokens)
		msState.totalLatencyMS += ms
		if providerID != "" {
			msState.lastProvider = providerID
		}
		msState.lastUsedAt = now
	}
}

// RecordError records a failed inference attempt along with HTTP status, latency, and error message.
func (m *Metrics) RecordError(providerID, clientModel string, status int, latency time.Duration, errMsg string) {
	if m == nil {
		return
	}
	ms := latency.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	now := time.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests++
	m.errors++

	if providerID != "" {
		ps := m.ensureProviderLocked(providerID)
		ps.requests++
		ps.errors++
		ps.totalLatencyMS += ms
		ps.lastStatus = status
		ps.lastError = errMsg
		ps.lastUsedAt = now
	}

	if clientModel != "" {
		msState := m.ensureModelLocked(clientModel)
		msState.requests++
		msState.errors++
		msState.totalLatencyMS += ms
		if providerID != "" {
			msState.lastProvider = providerID
		}
		msState.lastUsedAt = now
	}
}

// ProviderSnapshot returns current metrics for providerID, or nil if no requests have hit it yet.
func (m *Metrics) ProviderSnapshot(providerID string) *ProviderMetricSnapshot {
	if m == nil || providerID == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	ps, ok := m.byProvider[providerID]
	if !ok || ps == nil {
		return nil
	}
	snap := toProviderSnapshot(providerID, ps)
	return &snap
}

// Snapshot returns a sorted point-in-time view of all recorded gateway telemetry.
func (m *Metrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := MetricsSnapshot{
		StartedAt:       m.startedAt.Format(time.RFC3339),
		Requests:        m.requests,
		Success:         m.success,
		Errors:          m.errors,
		InputTokens:     m.inputTokens,
		OutputTokens:    m.outputTokens,
		CachedTokens:    m.cachedTokens,
		ReasoningTokens: m.reasoningTokens,
		Providers:       make([]ProviderMetricSnapshot, 0, len(m.byProvider)),
		Models:          make([]ModelMetricSnapshot, 0, len(m.byModel)),
	}

	provKeys := make([]string, 0, len(m.byProvider))
	for k := range m.byProvider {
		provKeys = append(provKeys, k)
	}
	sort.Strings(provKeys)
	for _, k := range provKeys {
		out.Providers = append(out.Providers, toProviderSnapshot(k, m.byProvider[k]))
	}

	modelKeys := make([]string, 0, len(m.byModel))
	for k := range m.byModel {
		modelKeys = append(modelKeys, k)
	}
	sort.Strings(modelKeys)
	for _, k := range modelKeys {
		st := m.byModel[k]
		avg := int64(0)
		if st.requests > 0 {
			avg = st.totalLatencyMS / st.requests
		}
		msSnap := ModelMetricSnapshot{
			Model:        k,
			Requests:     st.requests,
			Success:      st.success,
			Errors:       st.errors,
			InputTokens:  st.inputTokens,
			OutputTokens: st.outputTokens,
			AvgLatencyMS: avg,
			LastProvider: st.lastProvider,
		}
		if !st.lastUsedAt.IsZero() {
			msSnap.LastUsedAt = st.lastUsedAt.Format(time.RFC3339)
		}
		out.Models = append(out.Models, msSnap)
	}
	return out
}

// Reset clears all collected counters and resets StartedAt.
func (m *Metrics) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startedAt = time.Now().UTC()
	m.requests = 0
	m.success = 0
	m.errors = 0
	m.inputTokens = 0
	m.outputTokens = 0
	m.cachedTokens = 0
	m.reasoningTokens = 0
	m.byProvider = make(map[string]*providerMetricState)
	m.byModel = make(map[string]*modelMetricState)
}

func (m *Metrics) ensureProviderLocked(id string) *providerMetricState {
	ps, ok := m.byProvider[id]
	if !ok {
		ps = &providerMetricState{}
		m.byProvider[id] = ps
	}
	return ps
}

func (m *Metrics) ensureModelLocked(model string) *modelMetricState {
	ms, ok := m.byModel[model]
	if !ok {
		ms = &modelMetricState{}
		m.byModel[model] = ms
	}
	return ms
}

func toProviderSnapshot(id string, ps *providerMetricState) ProviderMetricSnapshot {
	avg := int64(0)
	if ps.requests > 0 {
		avg = ps.totalLatencyMS / ps.requests
	}
	snap := ProviderMetricSnapshot{
		ProviderID:      id,
		Requests:        ps.requests,
		Success:         ps.success,
		Errors:          ps.errors,
		InputTokens:     ps.inputTokens,
		OutputTokens:    ps.outputTokens,
		CachedTokens:    ps.cachedTokens,
		ReasoningTokens: ps.reasoningTokens,
		AvgLatencyMS:    avg,
		EWMALatencyMS:   int64(ps.ewmaLatencyMS + 0.5),
		LastStatus:      ps.lastStatus,
		LastError:       ps.lastError,
	}
	if !ps.lastUsedAt.IsZero() {
		snap.LastUsedAt = ps.lastUsedAt.Format(time.RFC3339)
	}
	return snap
}
