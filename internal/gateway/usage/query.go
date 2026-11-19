package usage

import (
	"fmt"
	"sort"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"gorm.io/gorm"
)

// Pricer prices usage of model on provider; ok=false when no price is known.
type Pricer func(provider, model string, u ir.Usage) (usd float64, ok bool)

// Query selects ledger rows for Summarize.
type Query struct {
	From, To time.Time
	GroupBy  string // provider | model | caller | day | key
	Caller   string
	Provider string
}

// Row is one summary group.
type Row struct {
	Key        string  `json:"key"`
	Requests   int64   `json:"requests"`
	Errors     int64   `json:"errors"`
	Input      int64   `json:"input_tokens"`
	Output     int64   `json:"output_tokens"`
	CacheRead  int64   `json:"cache_read_tokens"`
	CacheWrite int64   `json:"cache_write_tokens"`
	Reasoning  int64   `json:"reasoning_tokens"`
	CostUSD    float64 `json:"cost_usd"`
	Unpriced   int64   `json:"unpriced_requests,omitempty"` // requests whose model has no known price
	AvgLatency int64   `json:"avg_latency_ms"`
}

// Summary is Summarize's answer.
type Summary struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	GroupBy string    `json:"group_by"`
	Rows    []Row     `json:"rows"`
	Total   Row       `json:"total"`
}

var groupCols = map[string]string{
	"provider": "provider",
	"model":    "client_model",
	"caller":   "caller_name",
	"key":      "provider || '#' || key_fp",
	"day":      "strftime('%Y-%m-%d', ts / 1000, 'unixepoch')",
}

type aggRow struct {
	G          string
	Provider   string
	Model      string
	Requests   int64
	Errors     int64
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Reasoning  int64
	Latency    int64
}

// Summarize groups ledger rows and prices each (provider, upstream model) bucket with price.
func Summarize(db *gorm.DB, q Query, price Pricer) (Summary, error) {
	col, ok := groupCols[q.GroupBy]
	if !ok {
		return Summary{}, fmt.Errorf("unsupported group_by %q (provider, model, caller, key, day)", q.GroupBy)
	}
	if q.To.IsZero() {
		q.To = time.Now()
	}
	if q.From.IsZero() {
		q.From = q.To.Add(-24 * time.Hour)
	}
	tx := db.Model(&models.UsageRecord{}).
		Select(col+` AS g, provider, COALESCE(NULLIF(upstream_model, ''), client_model) AS model,
			COUNT(*) AS requests, SUM(CASE WHEN status >= 400 OR error != '' THEN 1 ELSE 0 END) AS errors,
			SUM(input_tokens) AS input, SUM(output_tokens) AS output, SUM(cache_read) AS cache_read,
			SUM(cache_write) AS cache_write, SUM(reasoning) AS reasoning, SUM(latency_ms) AS latency`).
		Where("ts >= ? AND ts <= ?", q.From.UnixMilli(), q.To.UnixMilli())
	if q.Caller != "" {
		tx = tx.Where("caller_name = ?", q.Caller)
	}
	if q.Provider != "" {
		tx = tx.Where("provider = ?", q.Provider)
	}
	var aggs []aggRow
	if err := tx.Group("g, provider, model").Scan(&aggs).Error; err != nil {
		return Summary{}, err
	}

	byKey := map[string]*Row{}
	total := Row{Key: "total"}
	for _, a := range aggs {
		row := byKey[a.G]
		if row == nil {
			row = &Row{Key: a.G}
			byKey[a.G] = row
		}
		u := ir.Usage{InputTokens: int(a.Input), OutputTokens: int(a.Output), CacheReadTokens: int(a.CacheRead), CacheWriteTokens: int(a.CacheWrite)}
		cost, priced := 0.0, false
		if price != nil && a.Provider != "" {
			cost, priced = price(a.Provider, a.Model, u)
		}
		for _, r := range []*Row{row, &total} {
			r.Requests += a.Requests
			r.Errors += a.Errors
			r.Input += a.Input
			r.Output += a.Output
			r.CacheRead += a.CacheRead
			r.CacheWrite += a.CacheWrite
			r.Reasoning += a.Reasoning
			r.AvgLatency += a.Latency // summed here, divided below
			r.CostUSD += cost
			if !priced && a.Requests > a.Errors {
				r.Unpriced += a.Requests - a.Errors
			}
		}
	}
	out := Summary{From: q.From, To: q.To, GroupBy: q.GroupBy, Total: total}
	for _, r := range byKey {
		out.Rows = append(out.Rows, *r)
	}
	finish := func(r *Row) {
		if r.Requests > 0 {
			r.AvgLatency /= r.Requests
		}
	}
	for i := range out.Rows {
		finish(&out.Rows[i])
	}
	finish(&out.Total)
	sort.Slice(out.Rows, func(i, j int) bool {
		if q.GroupBy == "day" {
			return out.Rows[i].Key < out.Rows[j].Key
		}
		if out.Rows[i].CostUSD != out.Rows[j].CostUSD {
			return out.Rows[i].CostUSD > out.Rows[j].CostUSD
		}
		return out.Rows[i].Requests > out.Rows[j].Requests
	})
	return out, nil
}

// SpendSince sums the priced cost of rows matching where/args since from (spend caps).
func SpendSince(db *gorm.DB, from time.Time, price Pricer, where string, args ...any) (float64, int64, error) {
	var aggs []aggRow
	tx := db.Model(&models.UsageRecord{}).
		Select(`provider, COALESCE(NULLIF(upstream_model, ''), client_model) AS model,
			SUM(input_tokens) AS input, SUM(output_tokens) AS output, SUM(cache_read) AS cache_read, SUM(cache_write) AS cache_write`).
		Where("ts >= ?", from.UnixMilli())
	if where != "" {
		tx = tx.Where(where, args...)
	}
	if err := tx.Group("provider, model").Scan(&aggs).Error; err != nil {
		return 0, 0, err
	}
	var usd float64
	var tokens int64
	for _, a := range aggs {
		tokens += a.Input + a.Output
		if price != nil {
			if c, ok := price(a.Provider, a.Model, ir.Usage{InputTokens: int(a.Input), OutputTokens: int(a.Output),
				CacheReadTokens: int(a.CacheRead), CacheWriteTokens: int(a.CacheWrite)}); ok {
				usd += c
			}
		}
	}
	return usd, tokens, nil
}
