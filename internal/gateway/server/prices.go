package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/modelmeta"
	"gorm.io/gorm/clause"
)

// EnableModelMeta attaches the models.dev catalogue (pricing, context windows, modalities) and
// starts keeping it fresh in the background.
func (s *Server) EnableModelMeta(ctx context.Context, opts modelmeta.Options) {
	opts.Overrides = s.priceOverride
	s.reloadPrices()
	s.Meta = modelmeta.New(opts)
	s.Meta.Start(ctx)
}

// lookupMeta returns model metadata for a provider/model pair (false when unknown).
func (s *Server) lookupMeta(providerID, model string) (modelmeta.Meta, bool) {
	if s.Meta == nil {
		return modelmeta.Meta{}, false
	}
	preset := ""
	if p, ok := s.Registry.Get(providerID); ok {
		preset = p.Preset
	}
	return s.Meta.Lookup(preset, providerID, model)
}

func priceKey(providerID, model string) string { return providerID + "\x00" + model }

func (s *Server) reloadPrices() {
	if s.DB == nil {
		return
	}
	var rows []models.ModelPrice
	if err := s.DB.Find(&rows).Error; err != nil {
		log.Printf("⚠️ failed to load price overrides: %v", err)
		return
	}
	m := make(map[string]modelmeta.Meta, len(rows))
	for _, r := range rows {
		m[priceKey(r.Provider, r.Model)] = modelmeta.Meta{
			Input: r.Input, Output: r.Output, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite,
			HasPrice: true, Source: "override",
		}
	}
	s.pricesMu.Lock()
	s.prices = m
	s.pricesMu.Unlock()
}

func (s *Server) priceOverride(providerID, model string) (modelmeta.Meta, bool) {
	s.pricesMu.RLock()
	defer s.pricesMu.RUnlock()
	if m, ok := s.prices[priceKey(providerID, model)]; ok {
		return m, true
	}
	m, ok := s.prices[priceKey("*", model)]
	return m, ok
}

// HandleListPrices serves GET /api/gateway/prices: overrides plus catalogue status, and with
// ?provider=&model= the effective metadata for one model.
func (s *Server) HandleListPrices(w http.ResponseWriter, r *http.Request) {
	if model := strings.TrimSpace(r.URL.Query().Get("model")); model != "" {
		providerID := strings.TrimSpace(r.URL.Query().Get("provider"))
		meta, ok := s.lookupMeta(providerID, model)
		writeJSON(w, http.StatusOK, map[string]any{"provider": providerID, "model": model, "found": ok, "meta": meta})
		return
	}
	var rows []models.ModelPrice
	if s.DB != nil {
		s.DB.Order("provider, model").Find(&rows)
	}
	status := map[string]any{"disabled": true}
	if s.Meta != nil {
		status = s.Meta.Status()
	}
	writeJSON(w, http.StatusOK, map[string]any{"overrides": rows, "catalog": status})
}

// HandlePutPrice serves PUT /api/gateway/prices (upsert one override).
func (s *Server) HandlePutPrice(w http.ResponseWriter, r *http.Request) {
	var body models.ModelPrice
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request JSON: " + err.Error()})
		return
	}
	body.Provider = strings.ToLower(strings.TrimSpace(body.Provider))
	body.Model = strings.TrimSpace(body.Model)
	if body.Provider == "" {
		body.Provider = "*"
	}
	if body.Model == "" || body.Input < 0 || body.Output < 0 || body.CacheRead < 0 || body.CacheWrite < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model is required and prices must be >= 0"})
		return
	}
	body.ID = 0
	err := s.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "provider"}, {Name: "model"}},
		DoUpdates: clause.AssignmentColumns([]string{"input", "output", "cache_read", "cache_write", "updated_at"}),
	}).Create(&body).Error
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.reloadPrices()
	writeJSON(w, http.StatusOK, map[string]any{"override": body})
}

// HandleDeletePrice serves DELETE /api/gateway/prices?provider=&model=.
func (s *Server) HandleDeletePrice(w http.ResponseWriter, r *http.Request) {
	providerID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	if providerID == "" {
		providerID = "*"
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	res := s.DB.Where("provider = ? AND model = ?", providerID, model).Delete(&models.ModelPrice{})
	if res.Error != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": res.Error.Error()})
		return
	}
	s.reloadPrices()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": res.RowsAffected})
}
