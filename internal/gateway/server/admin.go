package server

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/router"
)

type adminProviderDTO struct {
	ID             string                     `json:"id"`
	Preset         string                     `json:"preset"`
	DisplayName    string                     `json:"display_name"`
	Enabled        bool                       `json:"enabled"`
	Available      bool                       `json:"available"`
	NoKey          bool                       `json:"no_key"`
	FreeOnly       bool                       `json:"free_only,omitempty"`
	IncludeNonChat bool                       `json:"include_non_chat,omitempty"`
	KeyStrategy    string                     `json:"key_strategy"`
	KeyCount       int                        `json:"key_count"`
	MaskedKeys     []string                   `json:"masked_keys,omitempty"`
	KeyStatuses    []provider.KeyStatus       `json:"key_statuses,omitempty"`
	Prefer         string                     `json:"prefer"`
	Native         []string                   `json:"native"`
	Endpoints      provider.Endpoints         `json:"endpoints"`
	Cooldown       *router.CooldownInfo       `json:"cooldown,omitempty"`
	Stats          *ProviderMetricSnapshot    `json:"stats,omitempty"`
	ModelCount     int                        `json:"model_count"`
	CatalogFetched string                     `json:"catalog_fetched_at,omitempty"`
	CatalogError   string                     `json:"catalog_error,omitempty"`
	Models         []string                   `json:"models,omitempty"`
	Persisted      bool                       `json:"persisted_override,omitempty"`
	Unredacted     bool                       `json:"unredacted,omitempty"`
	ExtraBody      map[string]json.RawMessage `json:"extra_body,omitempty"`
	SystemPrompts  map[string]string          `json:"system_prompts,omitempty"`
}

type adminCandidateDTO struct {
	ID          uint                 `json:"id,omitempty"`
	Provider    string               `json:"provider"`
	TargetModel string               `json:"target_model"`
	Priority    int                  `json:"priority"`
	Source      string               `json:"source"`
	MatchKind   string               `json:"match_kind,omitempty"`
	Available   bool                 `json:"available"`
	Legacy      bool                 `json:"legacy,omitempty"`
	FreeOnly    bool                 `json:"free_only,omitempty"`
	Native      []string             `json:"native,omitempty"`
	LatencyMS   int64                `json:"latency_ms,omitempty"`
	Cooldown    *router.CooldownInfo `json:"cooldown,omitempty"`
}

type adminModelViewDTO struct {
	ClientModel    string              `json:"client_model"`
	CanonicalID    string              `json:"canonical_id,omitempty"`
	Strategy       string              `json:"strategy"`
	ActiveProvider string              `json:"active_provider,omitempty"`
	MultiBackend   bool                `json:"multi_backend,omitempty"`
	Candidates     []adminCandidateDTO `json:"candidates"`
}

type adminMatrixEntryDTO struct {
	CanonicalID    string              `json:"canonical_id"`
	DisplayName    string              `json:"display_name"`
	PrimaryModel   string              `json:"primary_model"`
	ActiveProvider string              `json:"active_provider,omitempty"`
	Strategy       string              `json:"strategy"`
	BackendCount   int                 `json:"backend_count"`
	MultiBackend   bool                `json:"multi_backend"`
	RawIDs         []string            `json:"raw_ids,omitempty"`
	Backends       []adminCandidateDTO `json:"backends"`
}

// HandleListProviders serves GET /api/gateway/providers.
func (s *Server) HandleListProviders(w http.ResponseWriter, r *http.Request) {
	cooldowns := s.Cooldown.Snapshot()
	list := s.Registry.List()
	out := make([]adminProviderDTO, 0, len(list))

	persisted, _ := loadProviderOverrides(s.DB)
	for _, p := range list {
		dto := s.buildProviderDTO(p, cooldowns)
		dto.Persisted = !persisted[p.ID].empty()
		out = append(out, dto)
	}

	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func (s *Server) buildProviderDTO(p *provider.Provider, cooldowns map[string]router.CooldownInfo) adminProviderDTO {
	native := make([]string, 0, len(p.Native))
	for _, proto := range p.Native {
		native = append(native, string(proto))
	}
	dto := adminProviderDTO{
		ID:             p.ID,
		Preset:         p.Preset,
		DisplayName:    p.DisplayName,
		Enabled:        p.IsEnabled(),
		Available:      p.Available(),
		NoKey:          p.NoKey,
		FreeOnly:       p.IsFreeOnly(),
		IncludeNonChat: p.IsIncludeNonChat(),
		KeyStrategy:    p.GetKeyStrategy(),
		Prefer:         string(p.PreferredProtocol()),
		Native:         native,
		Endpoints:      p.Endpoints,
		Stats:          s.Metrics.ProviderSnapshot(p.ID),
		Unredacted:     p.IsUnredacted(),
		ExtraBody:      p.GetExtraBody(),
		SystemPrompts:  p.GetSystemPrompts(),
	}
	if p.Keys != nil {
		dto.KeyCount = p.Keys.Count()
		dto.MaskedKeys = p.Keys.MaskedKeys()
		dto.KeyStatuses = p.Keys.StatusSnapshot()
	}
	if cd, ok := cooldowns[p.ID]; ok {
		cdCopy := cd
		dto.Cooldown = &cdCopy
	}
	if s.Registry.Catalog() != nil {
		catModels, fetchedAt, lastErr := s.Registry.Catalog().Snapshot(p.ID)
		dto.ModelCount = len(catModels)
		if !fetchedAt.IsZero() {
			dto.CatalogFetched = fetchedAt.UTC().Format(time.RFC3339)
		}
		dto.CatalogError = lastErr
		for _, cm := range catModels {
			dto.Models = append(dto.Models, cm.ID)
		}
	}
	return dto
}

// HandleUpdateProvider serves PATCH /api/gateway/providers/{id} to update runtime
// provider flags (enabled, free_only, include_non_chat, prefer), append an API key,
// clear cooldowns, and refresh the catalog when filtering or credentials changed.
func (s *Server) HandleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "id")))
	p, ok := s.Registry.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown provider: " + id})
		return
	}

	var body struct {
		Enabled        *bool   `json:"enabled"`
		FreeOnly       *bool   `json:"free_only"`
		IncludeNonChat *bool   `json:"include_non_chat"`
		Prefer         *string `json:"prefer"`
		KeyStrategy    *string `json:"key_strategy"`
		AddAPIKey      string  `json:"add_api_key"`
		RemoveAPIKey   string  `json:"remove_api_key"`
		ClearCooldown  bool    `json:"clear_cooldown"`
		Refresh        bool    `json:"refresh"`
		Unredacted     *bool   `json:"unredacted"`
		// extra_body / system_prompts replace the whole map; null or {} clears it
		ExtraBody     json.RawMessage `json:"extra_body"`
		SystemPrompts json.RawMessage `json:"system_prompts"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request JSON: " + err.Error()})
		return
	}

	var preferProto *ir.Protocol
	if body.Prefer != nil {
		trimmed := strings.ToLower(strings.TrimSpace(*body.Prefer))
		if trimmed != "" {
			pr := ir.Protocol(trimmed)
			switch pr {
			case ir.ProtoOpenAIChat, ir.ProtoOpenAIResp, ir.ProtoAnthropic, ir.ProtoGemini:
				preferProto = &pr
			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported prefer protocol: " + *body.Prefer})
				return
			}
		}
	}

	if body.KeyStrategy != nil {
		ks := strings.ToLower(strings.TrimSpace(*body.KeyStrategy))
		switch ks {
		case "sticky", "round-robin", "round_robin", "roundrobin", "rr", "":
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported key_strategy: " + *body.KeyStrategy})
			return
		}
	}

	var extraBody *map[string]json.RawMessage
	if len(body.ExtraBody) > 0 {
		m := map[string]json.RawMessage{}
		if string(body.ExtraBody) != "null" {
			if err := json.Unmarshal(body.ExtraBody, &m); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "extra_body must be an object: " + err.Error()})
				return
			}
		}
		extraBody = &m
	}
	var systemPrompts *map[string]string
	if len(body.SystemPrompts) > 0 {
		m := map[string]string{}
		if string(body.SystemPrompts) != "null" {
			if err := json.Unmarshal(body.SystemPrompts, &m); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "system_prompts must map model patterns to text: " + err.Error()})
				return
			}
		}
		systemPrompts = &m
	}

	removedRawKey := ""
	if rm := strings.TrimSpace(body.RemoveAPIKey); rm != "" && p.Keys != nil {
		removedRawKey, _ = p.Keys.FindByMasked(rm)
	}

	catalogAffected, err := p.ApplyRuntimePatchOptions(provider.RuntimePatchOptions{
		Enabled:        body.Enabled,
		FreeOnly:       body.FreeOnly,
		IncludeNonChat: body.IncludeNonChat,
		Prefer:         preferProto,
		KeyStrategy:    body.KeyStrategy,
		AddAPIKey:      body.AddAPIKey,
		RemoveAPIKey:   body.RemoveAPIKey,
		Unredacted:     body.Unredacted,
		ExtraBody:      extraBody,
		SystemPrompts:  systemPrompts,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	persistErr := s.persistProviderPatch(p.ID, providerPatch{
		Enabled:        body.Enabled,
		FreeOnly:       body.FreeOnly,
		IncludeNonChat: body.IncludeNonChat,
		Prefer:         preferProto,
		KeyStrategy:    body.KeyStrategy,
		AddedKey:       body.AddAPIKey,
		RemovedKey:     removedRawKey,
		Unredacted:     body.Unredacted,
		ExtraBody:      p.GetExtraBodyIf(extraBody != nil),
		SystemPrompts:  p.GetSystemPromptsIf(systemPrompts != nil),
	})
	if persistErr != nil {
		log.Printf("⚠️ Failed to persist runtime overrides for provider %s: %v", p.ID, persistErr)
	}

	if body.ClearCooldown || (body.Enabled != nil && *body.Enabled) || strings.TrimSpace(body.AddAPIKey) != "" {
		s.Cooldown.Clear(p.ID)
		if p.Keys != nil {
			p.Keys.ClearCooldowns()
		}
	}
	if (catalogAffected || body.Refresh) && p.Available() && s.Registry.Catalog() != nil {
		_, _ = s.Registry.Catalog().Refresh(r.Context(), p)
	}

	dto := s.buildProviderDTO(p, s.Cooldown.Snapshot())
	dto.Persisted = persistErr == nil && s.hasPersistedOverride(p.ID)
	resp := map[string]any{"provider": dto}
	if persistErr != nil {
		resp["persist_error"] = persistErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleGatewayStats serves GET /api/gateway/stats.
func (s *Server) HandleGatewayStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Metrics.Snapshot())
}

// HandleResetGatewayStats serves POST /api/gateway/stats/reset.
func (s *Server) HandleResetGatewayStats(w http.ResponseWriter, _ *http.Request) {
	s.Metrics.Reset()
	writeJSON(w, http.StatusOK, s.Metrics.Snapshot())
}

// HandleRefreshProvider serves POST /api/gateway/providers/{id}/refresh.
func (s *Server) HandleRefreshProvider(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "id")))
	p, ok := s.Registry.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown provider: " + id})
		return
	}
	if !p.Available() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider is disabled or has no credentials configured"})
		return
	}

	catModels, err := s.Registry.Catalog().Refresh(r.Context(), p)
	ids := make([]string, 0, len(catModels))
	for _, m := range catModels {
		ids = append(ids, m.ID)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"provider": id,
			"error":    err.Error(),
			"models":   ids,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider":    id,
		"model_count": len(ids),
		"models":      ids,
	})
}

// HandleListGatewayModels serves GET /api/gateway/models.
func (s *Server) HandleListGatewayModels(w http.ResponseWriter, r *http.Request) {
	if qModel := strings.TrimSpace(r.URL.Query().Get("model")); qModel != "" {
		view := s.buildModelView(r, qModel)
		writeJSON(w, http.StatusOK, view)
		return
	}

	grouped := db.GetAllModelCandidates()
	cooldowns := s.Cooldown.Snapshot()

	modelSet := make(map[string]bool, len(grouped))
	for k := range grouped {
		modelSet[k] = true
	}
	for _, p := range s.Registry.Available() {
		catModels, _, _ := s.Registry.Catalog().Snapshot(p.ID)
		for _, m := range catModels {
			modelSet[m.ID] = true
		}
	}

	names := make([]string, 0, len(modelSet))
	for name := range modelSet {
		names = append(names, name)
	}
	sort.Strings(names)

	views := make([]adminModelViewDTO, 0, len(names))
	for _, name := range names {
		views = append(views, s.buildModelViewWithData(name, grouped[name], cooldowns))
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": views})
}

// HandleGetGatewayModel serves GET /api/gateway/models/{client}.
func (s *Server) HandleGetGatewayModel(w http.ResponseWriter, r *http.Request) {
	client := resolveClientModelParam(r, "")
	if client == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "client model is required"})
		return
	}
	view := s.buildModelView(r, client)
	writeJSON(w, http.StatusOK, view)
}

// HandleSelectModelBackend serves POST /api/gateway/models/{client}/select and POST /api/gateway/select.
func (s *Server) HandleSelectModelBackend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientModel   string `json:"client_model"`
		Provider      string `json:"provider"`
		TargetModel   string `json:"target_model"`
		ClearCooldown *bool  `json:"clear_cooldown"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request JSON: " + err.Error()})
		return
	}
	client := resolveClientModelParam(r, body.ClientModel)
	provID := strings.ToLower(strings.TrimSpace(body.Provider))
	if client == "" || provID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "client_model and provider are required"})
		return
	}

	if !s.Registry.Has(provID) && provID != "google" && provID != "codex" && provID != "vertex" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown provider: " + provID})
		return
	}
	if err := db.ValidateRouteProvider(client, provID); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	if err := db.SelectModelProvider(s.DB, client, provID, body.TargetModel); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if body.ClearCooldown == nil || *body.ClearCooldown {
		s.Cooldown.Clear(provID)
	}

	writeJSON(w, http.StatusOK, s.buildModelView(r, client))
}

// HandleSetModelPolicy serves PUT /api/gateway/models/{client}/policy and PUT /api/gateway/policy.
func (s *Server) HandleSetModelPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientModel string `json:"client_model"`
		Strategy    string `json:"strategy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request JSON: " + err.Error()})
		return
	}
	client := resolveClientModelParam(r, body.ClientModel)
	if client == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "client_model is required"})
		return
	}
	if err := db.SetModelPolicy(s.DB, client, body.Strategy); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.buildModelView(r, client))
}

// HandleClearCooldown serves POST /api/gateway/cooldown/clear.
func (s *Server) HandleClearCooldown(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	provID := strings.ToLower(strings.TrimSpace(body.Provider))
	s.Cooldown.Clear(provID)
	if s.Registry != nil {
		if provID != "" {
			if p, ok := s.Registry.Get(provID); ok && p.Keys != nil {
				p.Keys.ClearCooldowns()
			}
		} else {
			for _, p := range s.Registry.List() {
				if p.Keys != nil {
					p.Keys.ClearCooldowns()
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cleared":   provID,
		"cooldowns": s.Cooldown.Snapshot(),
	})
}

// HandleSyncModelFailover serves POST /api/gateway/models/{client}/failover to materialize
// all discovered supporting backends for a model into priority-ordered failover DB routes.
func (s *Server) HandleSyncModelFailover(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientModel string   `json:"client_model"`
		Strategy    string   `json:"strategy"`
		Providers   []string `json:"providers"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&body)
	client := resolveClientModelParam(r, body.ClientModel)
	if client == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "client_model is required"})
		return
	}

	strategy := strings.TrimSpace(body.Strategy)
	if strategy == "" {
		strategy = models.StrategyFailover
	}
	if err := db.SetModelPolicy(s.DB, client, strategy); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	view := s.buildModelView(r, client)
	allowed := make(map[string]bool, len(body.Providers))
	for _, pid := range body.Providers {
		allowed[db.NormalizeProvider(pid)] = true
	}

	existing := make(map[string]bool)
	nextPriority := 1
	for _, c := range view.Candidates {
		if c.Source == "db" {
			existing[c.Provider] = true
			if c.Priority >= nextPriority {
				nextPriority = c.Priority + 1
			}
		}
	}

	for _, c := range view.Candidates {
		if c.Source == "db" || c.Legacy || !c.Available || existing[c.Provider] {
			continue
		}
		if len(allowed) > 0 && !allowed[c.Provider] {
			continue
		}
		if err := db.ValidateRouteProvider(client, c.Provider); err != nil {
			continue
		}
		_ = db.CreateModelRoute(s.DB, &models.ModelRoute{
			ClientModel:    client,
			TargetProvider: c.Provider,
			TargetModel:    c.TargetModel,
			Priority:       nextPriority,
			IsActive:       true,
		})
		existing[c.Provider] = true
		nextPriority++
	}

	writeJSON(w, http.StatusOK, s.buildModelView(r, client))
}

// HandleModelsMatrix serves GET /api/gateway/models/matrix, grouping models across providers
// by CanonicalModelID so callers and /ui can immediately list all backends supporting a given model.
func (s *Server) HandleModelsMatrix(w http.ResponseWriter, r *http.Request) {
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if query == "" {
		query = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("model")))
	}
	multiOnly := strings.EqualFold(r.URL.Query().Get("multi_only"), "true") || r.URL.Query().Get("multi_only") == "1"

	groupedRoutes := db.GetAllModelCandidates()
	cooldowns := s.Cooldown.Snapshot()

	type matrixAcc struct {
		canonID   string
		primary   string
		rawSet    map[string]bool
		byProv    map[string]adminCandidateDTO
		provOrder []string
	}
	groups := make(map[string]*matrixAcc)
	getOrCreate := func(rawID string) *matrixAcc {
		canon := provider.CanonicalModelID(rawID)
		if canon == "" {
			canon = strings.ToLower(strings.TrimSpace(rawID))
		}
		acc := groups[canon]
		if acc == nil {
			acc = &matrixAcc{
				canonID: canon,
				rawSet:  make(map[string]bool),
				byProv:  make(map[string]adminCandidateDTO),
			}
			groups[canon] = acc
		}
		if rawID != "" {
			acc.rawSet[rawID] = true
		}
		return acc
	}

	// 1. Seed explicit DB routes (highest priority).
	dbModelNames := make([]string, 0, len(groupedRoutes))
	for k := range groupedRoutes {
		dbModelNames = append(dbModelNames, k)
	}
	sort.Strings(dbModelNames)
	for _, clientName := range dbModelNames {
		acc := getOrCreate(clientName)
		if acc.primary == "" {
			acc.primary = clientName
		}
		for _, rt := range groupedRoutes[clientName] {
			provID := db.NormalizeProvider(rt.TargetProvider)
			dto := s.makeCandidateDTO(rt.ID, provID, rt.TargetModel, rt.Priority, "db", "db", cooldowns)
			if _, exists := acc.byProv[provID]; !exists {
				acc.provOrder = append(acc.provOrder, provID)
				acc.byProv[provID] = dto
			}
		}
	}

	// 2. Merge available provider catalogs under CanonicalModelID.
	if s.Registry != nil && s.Registry.Catalog() != nil {
		for _, p := range s.Registry.Available() {
			catModels, _, _ := s.Registry.Catalog().Snapshot(p.ID)
			for _, m := range catModels {
				acc := getOrCreate(m.ID)
				if _, exists := acc.byProv[p.ID]; exists {
					continue
				}
				matchKind := "canonical"
				if m.ID == acc.canonID {
					matchKind = "exact"
				}
				dto := s.makeCandidateDTO(0, p.ID, p.MapModel(m.ID), 100, "catalog", matchKind, cooldowns)
				acc.provOrder = append(acc.provOrder, p.ID)
				acc.byProv[p.ID] = dto
			}
		}
	}

	entries := make([]adminMatrixEntryDTO, 0, len(groups))
	multiCount := 0
	for _, acc := range groups {
		multi := len(acc.provOrder) >= 2
		if multi {
			multiCount++
		}
		if multiOnly && !multi {
			continue
		}

		raws := make([]string, 0, len(acc.rawSet))
		for raw := range acc.rawSet {
			raws = append(raws, raw)
		}
		sort.Strings(raws)

		primary := acc.primary
		if primary == "" {
			if multi {
				primary = acc.canonID
			} else if len(raws) > 0 {
				primary = raws[0]
			} else {
				primary = acc.canonID
			}
		}

		backends := make([]adminCandidateDTO, 0, len(acc.provOrder))
		activeProv := ""
		for _, pid := range acc.provOrder {
			cand := acc.byProv[pid]
			if activeProv == "" && cand.Available && cand.Cooldown == nil {
				activeProv = pid
			}
			backends = append(backends, cand)
		}
		if activeProv == "" {
			for _, cand := range backends {
				if cand.Available {
					activeProv = cand.Provider
					break
				}
			}
		}

		if query != "" && !matrixEntryMatchesQuery(acc.canonID, primary, raws, backends, query) {
			continue
		}

		entries = append(entries, adminMatrixEntryDTO{
			CanonicalID:    acc.canonID,
			DisplayName:    db.InferDisplayName(primary),
			PrimaryModel:   primary,
			ActiveProvider: activeProv,
			Strategy:       db.GetModelPolicy(primary),
			BackendCount:   len(backends),
			MultiBackend:   multi,
			RawIDs:         raws,
			Backends:       backends,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].BackendCount != entries[j].BackendCount {
			return entries[i].BackendCount > entries[j].BackendCount
		}
		return entries[i].CanonicalID < entries[j].CanonicalID
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"models":      entries,
		"total":       len(entries),
		"multi_count": multiCount,
	})
}

func matrixEntryMatchesQuery(canonID, primary string, raws []string, backends []adminCandidateDTO, q string) bool {
	if strings.Contains(strings.ToLower(canonID), q) || strings.Contains(strings.ToLower(primary), q) {
		return true
	}
	for _, r := range raws {
		if strings.Contains(strings.ToLower(r), q) {
			return true
		}
	}
	for _, b := range backends {
		if strings.Contains(strings.ToLower(b.Provider), q) || strings.Contains(strings.ToLower(b.TargetModel), q) {
			return true
		}
	}
	return false
}

func (s *Server) makeCandidateDTO(id uint, provID, targetModel string, priority int, source, matchKind string, cooldowns map[string]router.CooldownInfo) adminCandidateDTO {
	dto := adminCandidateDTO{
		ID:          id,
		Provider:    provID,
		TargetModel: targetModel,
		Priority:    priority,
		Source:      source,
		MatchKind:   matchKind,
	}
	if p, ok := s.Registry.Get(provID); ok {
		dto.Available = p.Available()
		dto.FreeOnly = p.IsFreeOnly()
		dto.Native = make([]string, 0, len(p.Native))
		for _, proto := range p.Native {
			dto.Native = append(dto.Native, string(proto))
		}
	} else if provID == "google" || provID == "codex" || provID == "vertex" {
		dto.Legacy = true
		if s.Router != nil {
			dto.Available = s.Router.IsLegacyAvailable(provID)
		}
	}
	if snap := s.Metrics.ProviderSnapshot(provID); snap != nil && snap.EWMALatencyMS > 0 {
		dto.LatencyMS = snap.EWMALatencyMS
	}
	if cd, ok := cooldowns[provID]; ok {
		cdCopy := cd
		dto.Cooldown = &cdCopy
	}
	return dto
}

func (s *Server) buildModelView(_ *http.Request, client string) adminModelViewDTO {
	routes := db.GetModelCandidates(client)
	cooldowns := s.Cooldown.Snapshot()
	return s.buildModelViewWithData(client, routes, cooldowns)
}

func (s *Server) buildModelViewWithData(client string, routes []models.ModelRoute, cooldowns map[string]router.CooldownInfo) adminModelViewDTO {
	strategy := db.GetModelPolicy(client)
	view := adminModelViewDTO{
		ClientModel: client,
		CanonicalID: provider.CanonicalModelID(client),
		Strategy:    strategy,
		Candidates:  make([]adminCandidateDTO, 0),
	}

	seenProv := make(map[string]bool)
	for _, rt := range routes {
		provID := db.NormalizeProvider(rt.TargetProvider)
		seenProv[provID] = true
		dto := s.makeCandidateDTO(rt.ID, provID, rt.TargetModel, rt.Priority, "db", "db", cooldowns)
		if view.ActiveProvider == "" && dto.Available && dto.Cooldown == nil {
			view.ActiveProvider = provID
		}
		view.Candidates = append(view.Candidates, dto)
	}

	// Surface exact and canonical catalog-discovered providers not already explicitly configured in DB routes.
	for _, p := range s.Registry.List() {
		if seenProv[p.ID] || s.Registry.Catalog() == nil {
			continue
		}
		upstreamID, ok := s.Registry.Catalog().SnapshotCanonicalMatch(p.ID, client)
		if !ok {
			continue
		}
		matchKind := "canonical"
		if upstreamID == client {
			matchKind = "exact"
		}
		dto := s.makeCandidateDTO(0, p.ID, p.MapModel(upstreamID), 100, "catalog", matchKind, cooldowns)
		if view.ActiveProvider == "" && dto.Available && dto.Cooldown == nil {
			view.ActiveProvider = p.ID
		}
		view.Candidates = append(view.Candidates, dto)
	}

	view.MultiBackend = len(view.Candidates) >= 2
	if view.ActiveProvider == "" {
		for _, cand := range view.Candidates {
			if cand.Available {
				view.ActiveProvider = cand.Provider
				break
			}
		}
	}
	return view
}

func resolveClientModelParam(r *http.Request, bodyVal string) string {
	if v := strings.TrimSpace(bodyVal); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.URL.Query().Get("model")); v != "" {
		return v
	}
	raw := chi.URLParam(r, "client")
	if raw == "" {
		raw = chi.URLParam(r, "*")
	}
	if unescaped, err := url.PathUnescape(raw); err == nil {
		raw = unescaped
	}
	return strings.TrimSpace(raw)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
