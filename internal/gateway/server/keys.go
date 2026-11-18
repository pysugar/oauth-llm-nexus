package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/clientkey"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
)

type adminClientKeyDTO struct {
	ID               uint               `json:"id"`
	Name             string             `json:"name"`
	MaskedKey        string             `json:"masked_key"`
	Legacy           bool               `json:"legacy,omitempty"`
	Disabled         bool               `json:"disabled"`
	Restricted       bool               `json:"restricted"`
	AllowedProviders []string           `json:"allowed_providers"`
	AllowedModels    []string           `json:"allowed_models"`
	CreatedAt        string             `json:"created_at,omitempty"`
	LastUsedAt       string             `json:"last_used_at,omitempty"`
	Stats            *KeyMetricSnapshot `json:"stats,omitempty"`
}

// clientKeyRequest is the body of POST and PATCH /api/gateway/keys. On PATCH, omitted (or null)
// fields stay unchanged and an empty array clears that allowlist.
type clientKeyRequest struct {
	Name             *string   `json:"name"`
	AllowedProviders *[]string `json:"allowed_providers"`
	AllowedModels    *[]string `json:"allowed_models"`
	Disabled         *bool     `json:"disabled"`
}

// legacyOnlyProviders are delegation targets served by legacy OAuth handlers rather than the
// gateway registry; they are valid allowlist entries even when not registered.
var legacyOnlyProviders = []string{"google", "codex", "vertex", "gemini"}

func (s *Server) clientKeyDTO(row models.ClientKey) adminClientKeyDTO {
	caller := clientkey.ToCaller(row)
	dto := adminClientKeyDTO{
		ID:               row.ID,
		Name:             row.Name,
		MaskedKey:        row.KeyHint,
		Disabled:         row.Disabled,
		Restricted:       caller.Restricted(),
		AllowedProviders: nonNil(caller.AllowedProviders),
		AllowedModels:    nonNil(caller.AllowedModels),
		CreatedAt:        row.CreatedAt.UTC().Format(time.RFC3339),
		Stats:            s.Metrics.KeySnapshot(row.Name),
	}
	if row.LastUsedAt != nil {
		dto.LastUsedAt = row.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return dto
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func (s *Server) validateAllowedProviders(ids []string) error {
	for _, id := range clientkey.NormalizeProviders(ids) {
		if (s.Registry != nil && s.Registry.Has(id)) || slices.Contains(legacyOnlyProviders, id) {
			continue
		}
		return fmt.Errorf("unknown provider %q in allowed_providers", id)
	}
	return nil
}

func isLegacyKeyRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	return ref == "0" || strings.EqualFold(ref, clientkey.LegacyName)
}

// HandleListClientKeys serves GET /api/gateway/keys: every client key (masked, never the secret)
// plus the read-only legacy key, each with in-memory request/token counters.
func (s *Server) HandleListClientKeys(w http.ResponseWriter, _ *http.Request) {
	out := make([]adminClientKeyDTO, 0)
	if legacy := db.GetAPIKey(s.DB); legacy != "" {
		out = append(out, adminClientKeyDTO{
			Name:             clientkey.LegacyName,
			MaskedKey:        clientkey.Mask(legacy),
			Legacy:           true,
			AllowedProviders: []string{},
			AllowedModels:    []string{},
			Stats:            s.Metrics.KeySnapshot(clientkey.LegacyName),
		})
	}
	rows, err := clientkey.List(s.DB)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, row := range rows {
		out = append(out, s.clientKeyDTO(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// HandleCreateClientKey serves POST /api/gateway/keys. The plaintext key is returned only here.
func (s *Server) HandleCreateClientKey(w http.ResponseWriter, r *http.Request) {
	var body clientKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request JSON: " + err.Error()})
		return
	}
	spec := clientkey.Spec{}
	if body.Name != nil {
		spec.Name = strings.TrimSpace(*body.Name)
	}
	if body.AllowedProviders != nil {
		spec.AllowedProviders = *body.AllowedProviders
	}
	if body.AllowedModels != nil {
		spec.AllowedModels = *body.AllowedModels
	}
	if err := s.validateAllowedProviders(spec.AllowedProviders); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	row, plaintext, err := clientkey.Create(s.DB, spec)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, clientkey.ErrDuplicateName) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	if body.Disabled != nil && *body.Disabled {
		if updated, err := clientkey.Update(s.DB, row.ID, clientkey.Patch{Disabled: body.Disabled}); err == nil {
			row = updated
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":     s.clientKeyDTO(*row),
		"api_key": plaintext,
		"note":    "store this key now; only its hash is kept and it cannot be shown again",
	})
}

// HandleUpdateClientKey serves PATCH /api/gateway/keys/{id} ({id} is the numeric id or the name).
func (s *Server) HandleUpdateClientKey(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "id")
	if isLegacyKeyRef(ref) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the legacy key is unrestricted; manage it via /api/config/apikey"})
		return
	}
	row, err := clientkey.Get(s.DB, ref)
	if err != nil {
		writeClientKeyLookupError(w, err)
		return
	}
	var body clientKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request JSON: " + err.Error()})
		return
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) != row.Name {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "renaming a key is not supported; create a new key instead"})
		return
	}
	if body.AllowedProviders != nil {
		if err := s.validateAllowedProviders(*body.AllowedProviders); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	updated, err := clientkey.Update(s.DB, row.ID, clientkey.Patch{
		AllowedProviders: body.AllowedProviders,
		AllowedModels:    body.AllowedModels,
		Disabled:         body.Disabled,
	})
	if err != nil {
		writeClientKeyLookupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.clientKeyDTO(*updated))
}

// HandleDeleteClientKey serves DELETE /api/gateway/keys/{id}; the key stops working immediately.
func (s *Server) HandleDeleteClientKey(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "id")
	if isLegacyKeyRef(ref) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the legacy key cannot be deleted; regenerate it via /api/config/apikey/regenerate"})
		return
	}
	row, err := clientkey.Get(s.DB, ref)
	if err != nil {
		writeClientKeyLookupError(w, err)
		return
	}
	if err := clientkey.Delete(s.DB, row.ID); err != nil {
		writeClientKeyLookupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": row.Name, "id": row.ID})
}

func writeClientKeyLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, clientkey.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}
