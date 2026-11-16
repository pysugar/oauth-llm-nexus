package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
)

type aggregatedModel struct {
	ID      string
	OwnedBy string
}

func (s *Server) collectModels(ctx context.Context) []aggregatedModel {
	seen := make(map[string]aggregatedModel)
	order := make([]string, 0)
	add := func(id, ownedBy string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = aggregatedModel{ID: id, OwnedBy: ownedBy}
		order = append(order, id)
	}

	// 1. Include all active configured ModelRoute client models first.
	candidatesByModel := db.GetAllModelCandidates()
	routedNames := make([]string, 0, len(candidatesByModel))
	for name := range candidatesByModel {
		routedNames = append(routedNames, name)
	}
	sort.Strings(routedNames)
	for _, name := range routedNames {
		cands := candidatesByModel[name]
		owned := "nexus"
		if len(cands) > 0 {
			owned = cands[0].TargetProvider
		}
		add(name, owned)
	}

	// 2. Merge live/cached models discovered from enabled credentialed gateway providers concurrently.
	if s.Registry != nil && s.Registry.Catalog() != nil {
		refreshCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		providers := s.Registry.Available()
		perProvider := make([][]provider.ModelInfo, len(providers))
		var wg sync.WaitGroup
		for i, p := range providers {
			wg.Add(1)
			go func(idx int, prov *provider.Provider) {
				defer wg.Done()
				perProvider[idx] = s.Registry.Catalog().EnsureFresh(refreshCtx, prov)
			}(i, p)
		}
		wg.Wait()
		for i, p := range providers {
			for _, m := range perProvider[i] {
				owned := m.OwnedBy
				if owned == "" {
					owned = p.ID
				}
				add(m.ID, owned)
			}
		}
	}

	out := make([]aggregatedModel, 0, len(order))
	for _, id := range order {
		out = append(out, seen[id])
	}
	return out
}

// HandleOpenAIModelsList serves GET /v1/models combining routed models and discovered backend catalogs.
func (s *Server) HandleOpenAIModelsList(w http.ResponseWriter, r *http.Request) {
	models := s.collectModels(r.Context())
	now := time.Now().Unix()
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{
			"id":       m.ID,
			"object":   "model",
			"created":  now,
			"owned_by": m.OwnedBy,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
	})
}

// HandleAnthropicModelsList serves GET /anthropic/v1/models and GET /v1/messages models metadata.
func (s *Server) HandleAnthropicModelsList(w http.ResponseWriter, r *http.Request) {
	models := s.collectModels(r.Context())
	nowISO := time.Now().UTC().Format(time.RFC3339)
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{
			"type":         "model",
			"id":           m.ID,
			"display_name": db.InferDisplayName(m.ID),
			"created_at":   nowISO,
		})
	}
	resp := map[string]any{
		"data":     data,
		"has_more": false,
	}
	if len(models) > 0 {
		resp["first_id"] = models[0].ID
		resp["last_id"] = models[len(models)-1].ID
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleGeminiModelsList serves GET /v1beta/models in Google Gemini discovery format.
func (s *Server) HandleGeminiModelsList(w http.ResponseWriter, r *http.Request) {
	models := s.collectModels(r.Context())
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{
			"name":        "models/" + m.ID,
			"baseModelId": m.ID,
			"displayName": db.InferDisplayName(m.ID),
			"supportedGenerationMethods": []string{
				"generateContent",
				"streamGenerateContent",
			},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"models": data,
	})
}
