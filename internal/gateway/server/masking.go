package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/redact"
	"gorm.io/gorm/clause"
)

// maskingConfigKey is the models.Config row holding persisted privacy masking settings.
const maskingConfigKey = "gateway_masking"

// MaskingKeyPath is where the placeholder key is kept: next to the database.
func MaskingKeyPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "nexus.redact.key")
}

// maskSettings are the persisted masking settings. Enabled, when set, wins over NEXUS_MASKING
// (persisted settings take precedence over environment defaults, as provider overrides do).
type maskSettings struct {
	Enabled        *bool                  `json:"enabled,omitempty"`
	Modes          map[string]redact.Mode `json:"modes,omitempty"`
	Allow          []string               `json:"allow,omitempty"`
	MaskEmbeddings bool                   `json:"mask_embeddings,omitempty"`
}

type maskState struct {
	masker   *redact.Masker
	err      error // why there is no masker: masking that is on fails closed
	settings atomic.Pointer[maskSettings]
	mu       sync.Mutex // serializes PUT read-modify-write
}

// envMasking reads NEXUS_MASKING: on/true/1 enables masking by default.
func envMasking() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NEXUS_MASKING"))) {
	case "on", "true", "1", "yes":
		return true
	}
	return false
}

// EnableMasking loads the placeholder key at keyPath and the persisted settings. A key that
// can't be loaded is remembered: masking, when on, then refuses every request (fail closed).
func (s *Server) EnableMasking(keyPath string) {
	st := &maskState{}
	st.masker, st.err = redact.New(keyPath, redact.DefaultCapacity)
	if st.err != nil {
		log.Printf("❌ privacy masking unavailable: %v", st.err)
	}
	set := &maskSettings{}
	if s.DB != nil {
		var rows []models.Config
		if err := s.DB.Where("key = ?", maskingConfigKey).Limit(1).Find(&rows).Error; err != nil {
			log.Printf("⚠️ failed to load masking settings: %v", err)
		} else if len(rows) > 0 && strings.TrimSpace(rows[0].Value) != "" {
			if err := json.Unmarshal([]byte(rows[0].Value), set); err != nil {
				log.Printf("⚠️ ignoring malformed masking settings: %v", err)
				set = &maskSettings{}
			}
		}
	}
	st.settings.Store(set)
	s.masking = st
	if s.maskingOn() {
		log.Printf("🔐 Privacy masking is on (key %s)", keyPath)
	}
}

func (s *Server) maskSettings() maskSettings {
	if s.masking != nil {
		if p := s.masking.settings.Load(); p != nil {
			return *p
		}
	}
	return maskSettings{}
}

// maskingOn reports whether requests are to be masked.
func (s *Server) maskingOn() bool {
	set := s.maskSettings()
	if set.Enabled != nil {
		return *set.Enabled
	}
	return envMasking()
}

func (s *Server) maskPolicy() redact.Policy {
	set := s.maskSettings()
	return redact.Policy{Modes: set.Modes, Allow: set.Allow}
}

// maskBody masks a client request body when masking is on. It returns the body to send on, the
// hits, and an error that ends the request: 503 when masking is on and can't run (nothing is
// sent unmasked), 403 when a value of a kind in block mode is present.
func (s *Server) maskBody(body []byte) ([]byte, []redact.Hit, *ir.GatewayError) {
	if !s.maskingOn() {
		return body, nil, nil
	}
	if s.masking == nil || s.masking.masker == nil {
		msg := "privacy masking is on but the masker is not available, so nothing was sent"
		if s.masking != nil && s.masking.err != nil {
			msg += ": " + s.masking.err.Error()
		}
		return nil, nil, ir.NewError(503, "masking_unavailable", msg)
	}
	out, hits, err := s.masking.masker.MaskJSON(body, s.maskPolicy())
	if errors.Is(err, redact.ErrNotJSON) {
		return nil, nil, ir.NewError(400, "invalid_json", "request body is not valid JSON")
	}
	if err != nil {
		log.Printf("❌ privacy masking failed, request refused: %v", err)
		return nil, nil, ir.NewError(503, "masking_unavailable", "privacy masking failed, so nothing was sent")
	}
	if redact.Blocked(hits) {
		return nil, hits, ir.NewError(403, "sensitive_content_blocked",
			"request holds values privacy masking is set to block: "+strings.Join(blockedKinds(hits), ", "))
	}
	for _, h := range hits {
		if h.Action == redact.ModeWarn {
			log.Printf("🔐 masking warn: %s (%d chars) sent as written", h.Kind, h.Len)
		}
	}
	return out, hits, nil
}

func blockedKinds(hits []redact.Hit) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hits {
		if h.Action == redact.ModeBlock && !seen[h.Kind] {
			seen[h.Kind] = true
			out = append(out, h.Kind)
		}
	}
	sort.Strings(out)
	return out
}

// restoringWriter wraps w so placeholders in the reply are put back; finish must run before the
// handler returns. Without masking on it is w itself.
func (s *Server) restoringWriter(w http.ResponseWriter, hits []redact.Hit) (http.ResponseWriter, func()) {
	if !s.maskingOn() || s.masking == nil || s.masking.masker == nil {
		return w, func() {}
	}
	if n := redact.Masked(hits); n > 0 {
		w.Header().Set("X-Nexus-Masked", strconv.Itoa(n))
	}
	rw := s.masking.masker.NewWriter(w)
	return rw, rw.Finish
}

// HandleGetMasking serves GET /api/gateway/masking.
func (s *Server) HandleGetMasking(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.maskingView())
}

func (s *Server) maskingView() map[string]any {
	set := s.maskSettings()
	source := "default"
	if set.Enabled != nil {
		source = "setting"
	} else if strings.TrimSpace(os.Getenv("NEXUS_MASKING")) != "" {
		source = "env"
	}
	modes := map[string]redact.Mode{}
	for _, k := range redact.Kinds {
		modes[k] = redact.ModeMask
		if m, ok := set.Modes[k]; ok && redact.ValidMode(m) {
			modes[k] = m
		}
	}
	view := map[string]any{
		"enabled":         s.maskingOn(),
		"source":          source,
		"modes":           modes,
		"allow":           append([]string{}, set.Allow...),
		"mask_embeddings": set.MaskEmbeddings,
		"available":       s.masking != nil && s.masking.masker != nil,
	}
	if s.masking != nil && s.masking.err != nil {
		view["error"] = s.masking.err.Error()
	}
	if s.masking != nil && s.masking.masker != nil {
		kinds, restored := s.masking.masker.Stats()
		view["stats"] = map[string]any{"kinds": kinds, "restored": restored, "remembered": s.masking.masker.Size()}
	}
	return view
}

// HandlePutMasking serves PUT /api/gateway/masking with any of {enabled, modes, allow,
// mask_embeddings}; "enabled": null forgets the persisted switch (NEXUS_MASKING applies again).
func (s *Server) HandlePutMasking(w http.ResponseWriter, r *http.Request) {
	if s.masking == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "masking is not initialized"})
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request JSON: " + err.Error()})
		return
	}
	s.masking.mu.Lock()
	defer s.masking.mu.Unlock()
	next := s.maskSettings()
	for k, raw := range body {
		var err error
		switch k {
		case "enabled":
			if string(raw) == "null" {
				next.Enabled = nil
				continue
			}
			var v bool
			if err = json.Unmarshal(raw, &v); err == nil {
				next.Enabled = &v
			}
		case "modes":
			var modes map[string]redact.Mode
			if err = json.Unmarshal(raw, &modes); err == nil {
				merged := map[string]redact.Mode{}
				for kind, m := range next.Modes {
					merged[kind] = m
				}
				for kind, m := range modes {
					kind = strings.ToUpper(strings.TrimSpace(kind))
					if !redact.IsKind(kind) {
						writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown kind: " + kind, "kinds": redact.Kinds})
						return
					}
					if !redact.ValidMode(m) {
						writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be mask, warn, block or off: " + string(m)})
						return
					}
					if m == redact.ModeMask {
						delete(merged, kind)
					} else {
						merged[kind] = m
					}
				}
				next.Modes = merged
			}
		case "allow":
			var allow []string
			if err = json.Unmarshal(raw, &allow); err == nil {
				next.Allow = next.Allow[:0:0]
				for _, a := range allow {
					if a = strings.TrimSpace(a); a != "" {
						next.Allow = append(next.Allow, a)
					}
				}
			}
		case "mask_embeddings":
			err = json.Unmarshal(raw, &next.MaskEmbeddings)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown field: " + k})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": k + ": " + err.Error()})
			return
		}
	}
	if s.DB != nil {
		raw, _ := json.Marshal(next)
		if err := s.DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
		}).Create(&models.Config{Key: maskingConfigKey, Value: string(raw)}).Error; err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	s.masking.settings.Store(&next)
	writeJSON(w, http.StatusOK, s.maskingView())
}

// HandleMaskingPreview serves POST /api/gateway/masking/preview {text}: what masking would send,
// with the hits (kinds and placeholders, never the values). Nothing is remembered.
func (s *Server) HandleMaskingPreview(w http.ResponseWriter, r *http.Request) {
	if s.masking == nil || s.masking.masker == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "masking is not available"})
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request JSON: " + err.Error()})
		return
	}
	out, hits := s.masking.masker.Preview(body.Text, s.maskPolicy())
	if hits == nil {
		hits = []redact.Hit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"masked": out, "hits": hits})
}
