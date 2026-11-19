package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// providerOverridesConfigKey is the models.Config row holding persisted runtime provider overrides.
const providerOverridesConfigKey = "gateway_provider_overrides"

// providerOverride captures runtime edits made via PATCH /api/gateway/providers/{id} so they
// survive restarts. Persisted values take precedence over providers.yaml and environment defaults.
// Keys added at runtime are stored verbatim in the local SQLite file (same trust boundary as the
// OAuth tokens it already holds); removed keys are stored only as SHA-256 fingerprints.
type providerOverride struct {
	Enabled        *bool    `json:"enabled,omitempty"`
	FreeOnly       *bool    `json:"free_only,omitempty"`
	IncludeNonChat *bool    `json:"include_non_chat,omitempty"`
	Prefer         string   `json:"prefer,omitempty"`
	KeyStrategy    string   `json:"key_strategy,omitempty"`
	AddedKeys      []string `json:"added_keys,omitempty"`
	RemovedKeys    []string `json:"removed_key_sha256,omitempty"`
	Unredacted     *bool    `json:"unredacted,omitempty"`
	// ExtraBody / SystemPrompts, when set, replace providers.yaml's (an empty map clears them).
	ExtraBody     *map[string]json.RawMessage `json:"extra_body,omitempty"`
	SystemPrompts *map[string]string          `json:"system_prompts,omitempty"`
}

func (o *providerOverride) empty() bool {
	return o == nil || (o.Enabled == nil && o.FreeOnly == nil && o.IncludeNonChat == nil &&
		o.Prefer == "" && o.KeyStrategy == "" && len(o.AddedKeys) == 0 && len(o.RemovedKeys) == 0 &&
		o.Unredacted == nil && o.ExtraBody == nil && o.SystemPrompts == nil)
}

func keyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])
}

func loadProviderOverrides(database *gorm.DB) (map[string]*providerOverride, error) {
	out := map[string]*providerOverride{}
	if database == nil {
		return out, nil
	}
	// Find+Limit instead of First: a missing row is the normal case and must not log as an error.
	var rows []models.Config
	if err := database.Where("key = ?", providerOverridesConfigKey).Limit(1).Find(&rows).Error; err != nil {
		return out, err
	}
	if len(rows) == 0 || strings.TrimSpace(rows[0].Value) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(rows[0].Value), &out); err != nil {
		return map[string]*providerOverride{}, err
	}
	return out, nil
}

func saveProviderOverrides(database *gorm.DB, all map[string]*providerOverride) error {
	if database == nil {
		return nil
	}
	for id, o := range all {
		if o.empty() {
			delete(all, id)
		}
	}
	raw, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return database.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&models.Config{Key: providerOverridesConfigKey, Value: string(raw)}).Error
}

// ApplyPersistedProviderOverrides replays persisted runtime overrides onto reg at startup.
// Call it before deriving any provider snapshots (compat catalog, catalog warmup).
func ApplyPersistedProviderOverrides(database *gorm.DB, reg *provider.Registry) ([]string, error) {
	all, err := loadProviderOverrides(database)
	if err != nil || reg == nil {
		return nil, err
	}
	var applied []string
	for id, o := range all {
		p, ok := reg.Get(id)
		if !ok || o.empty() {
			continue
		}
		applyOverride(p, o)
		applied = append(applied, id)
	}
	sort.Strings(applied)
	return applied, nil
}

func applyOverride(p *provider.Provider, o *providerOverride) {
	opts := provider.RuntimePatchOptions{
		Enabled:        o.Enabled,
		FreeOnly:       o.FreeOnly,
		IncludeNonChat: o.IncludeNonChat,
		Unredacted:     o.Unredacted,
		ExtraBody:      o.ExtraBody,
		SystemPrompts:  o.SystemPrompts,
	}
	if o.Prefer != "" {
		pr := ir.Protocol(o.Prefer)
		if p.EndpointFor(pr) != "" {
			opts.Prefer = &pr
		} else {
			log.Printf("⚠️ Ignoring persisted prefer=%s for provider %s (no endpoint)", o.Prefer, p.ID)
		}
	}
	if o.KeyStrategy != "" {
		ks := o.KeyStrategy
		opts.KeyStrategy = &ks
	}
	if _, err := p.ApplyRuntimePatchOptions(opts); err != nil {
		log.Printf("⚠️ Failed to apply persisted overrides for provider %s: %v", p.ID, err)
	}
	if p.Keys == nil {
		return
	}
	if len(o.AddedKeys) > 0 {
		p.Keys.AddKeys(o.AddedKeys...)
	}
	if len(o.RemovedKeys) > 0 {
		removed := make(map[string]bool, len(o.RemovedKeys))
		for _, h := range o.RemovedKeys {
			removed[h] = true
		}
		p.Keys.RemoveMatching(func(k string) bool { return removed[keyFingerprint(k)] })
	}
}

// providerPatch is the subset of a validated PATCH request that is persisted.
type providerPatch struct {
	Enabled        *bool
	FreeOnly       *bool
	IncludeNonChat *bool
	Prefer         *ir.Protocol
	KeyStrategy    *string
	AddedKey       string
	RemovedKey     string // raw key resolved from the masked label before removal
	Unredacted     *bool
	ExtraBody      *map[string]json.RawMessage // the provider's new extra_body (after validation)
	SystemPrompts  *map[string]string
}

// persistProviderPatch merges patch into the stored override for providerID.
func (s *Server) persistProviderPatch(providerID string, patch providerPatch) error {
	if s.DB == nil {
		return nil
	}
	s.overridesMu.Lock()
	defer s.overridesMu.Unlock()

	all, err := loadProviderOverrides(s.DB)
	if err != nil {
		return err
	}
	o := all[providerID]
	if o == nil {
		o = &providerOverride{}
	}
	if patch.Enabled != nil {
		o.Enabled = boolPtr(*patch.Enabled)
	}
	if patch.FreeOnly != nil {
		o.FreeOnly = boolPtr(*patch.FreeOnly)
	}
	if patch.IncludeNonChat != nil {
		o.IncludeNonChat = boolPtr(*patch.IncludeNonChat)
	}
	if patch.Prefer != nil && *patch.Prefer != "" {
		o.Prefer = string(*patch.Prefer)
	}
	if patch.KeyStrategy != nil && strings.TrimSpace(*patch.KeyStrategy) != "" {
		o.KeyStrategy = provider.NormalizeKeyStrategy(*patch.KeyStrategy)
	}
	if patch.Unredacted != nil {
		o.Unredacted = boolPtr(*patch.Unredacted)
	}
	if patch.ExtraBody != nil {
		o.ExtraBody = patch.ExtraBody
	}
	if patch.SystemPrompts != nil {
		o.SystemPrompts = patch.SystemPrompts
	}
	if k := strings.TrimSpace(patch.AddedKey); k != "" {
		o.RemovedKeys = removeString(o.RemovedKeys, keyFingerprint(k))
		if !containsString(o.AddedKeys, k) {
			o.AddedKeys = append(o.AddedKeys, k)
		}
	}
	if k := strings.TrimSpace(patch.RemovedKey); k != "" {
		if containsString(o.AddedKeys, k) {
			o.AddedKeys = removeString(o.AddedKeys, k)
		} else if fp := keyFingerprint(k); !containsString(o.RemovedKeys, fp) {
			o.RemovedKeys = append(o.RemovedKeys, fp)
		}
	}
	all[providerID] = o
	return saveProviderOverrides(s.DB, all)
}

func (s *Server) hasPersistedOverride(providerID string) bool {
	all, err := loadProviderOverrides(s.DB)
	if err != nil {
		return false
	}
	return !all[providerID].empty()
}

// HandleClearProviderOverrides serves DELETE /api/gateway/providers/{id}/overrides. It forgets the
// persisted runtime overrides; the live runtime state is untouched until the next restart, when
// providers.yaml / environment defaults apply again.
func (s *Server) HandleClearProviderOverrides(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "id")))
	if _, ok := s.Registry.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown provider: " + id})
		return
	}
	s.overridesMu.Lock()
	defer s.overridesMu.Unlock()
	all, err := loadProviderOverrides(s.DB)
	if err == nil {
		delete(all, id)
		err = saveProviderOverrides(s.DB, all)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider": id,
		"cleared":  true,
		"note":     "persisted overrides removed; config/env defaults apply after restart",
	})
}

func boolPtr(v bool) *bool { return &v }

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func removeString(list []string, v string) []string {
	out := list[:0]
	for _, s := range list {
		if s != v {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
