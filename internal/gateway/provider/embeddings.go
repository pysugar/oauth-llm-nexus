package provider

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

// IsEmbeddingModel reports whether modelID looks like a text embedding model
// (text-embedding-3-*, gemini-embedding-*, nomic-embed-text, nv-embedqa-*, bge-*, e5-*).
func IsEmbeddingModel(modelID string) bool {
	lower := strings.ToLower(strings.TrimSpace(modelID))
	if lower == "" {
		return false
	}
	if IsRerankModel(lower) {
		return false
	}
	if strings.Contains(lower, "embed") {
		return true
	}
	base := lower
	if idx := strings.LastIndexByte(base, '/'); idx >= 0 {
		base = base[idx+1:]
	}
	return strings.HasPrefix(base, "bge-") || strings.HasPrefix(base, "e5-") || strings.HasPrefix(base, "voyage-")
}

// SupportsEmbeddings reports whether p exposes an endpoint usable for text embeddings
// (OpenAI-compatible `/embeddings` or Gemini `:batchEmbedContents`).
func (p *Provider) SupportsEmbeddings() bool {
	return p.EmbeddingProtocol() != ""
}

// EmbeddingProtocol returns the upstream wire protocol used for embeddings, preferring
// OpenAI-compatible passthrough over Gemini-native translation.
func (p *Provider) EmbeddingProtocol() ir.Protocol {
	if p == nil {
		return ""
	}
	if p.EndpointFor(ir.ProtoOpenAIChat) != "" {
		return ir.ProtoOpenAIChat
	}
	if p.EndpointFor(ir.ProtoGemini) != "" {
		return ir.ProtoGemini
	}
	return ""
}

// EmbeddingModels returns cached embedding-capable models for p, refreshing the catalog when stale.
func (c *Catalog) EmbeddingModels(ctx context.Context, p *Provider) []ModelInfo {
	if c == nil || p == nil || !p.Available() {
		return nil
	}
	return c.auxModels(ctx, p, IsEmbeddingModel, func(e *catalogEntry) []ModelInfo { return e.embedModels })
}

// RerankModels returns cached rerank models for p (discovered or static).
func (c *Catalog) RerankModels(ctx context.Context, p *Provider) []ModelInfo {
	if c == nil || p == nil || !p.Available() {
		return nil
	}
	return c.auxModels(ctx, p, IsRerankModel, func(e *catalogEntry) []ModelInfo { return e.rerankModels })
}

func (c *Catalog) auxModels(ctx context.Context, p *Provider, is func(string) bool, pick func(*catalogEntry) []ModelInfo) []ModelInfo {
	var out []ModelInfo
	if !p.NoChat {
		_ = c.EnsureFresh(ctx, p)
		c.mu.RLock()
		if ent := c.entries[p.ID]; ent != nil {
			out = append(out, pick(ent)...)
		}
		c.mu.RUnlock()
	}
	for _, id := range p.StaticModels {
		if is(id) {
			out = append(out, ModelInfo{ID: id, ProviderID: p.ID, Static: true})
		}
	}
	return out
}

// IsRerankModel reports rerank models (rerank-2.5, jina-reranker-v2, nv-rerankqa, ...).
func IsRerankModel(modelID string) bool {
	return strings.Contains(strings.ToLower(modelID), "rerank")
}

// MatchRerankModel resolves clientModel against p's rerank models (exact, then canonical).
func (c *Catalog) MatchRerankModel(ctx context.Context, p *Provider, clientModel string) (upstreamID string, exact bool, ok bool) {
	return matchAux(c.RerankModels(ctx, p), clientModel)
}

func matchAux(list []ModelInfo, clientModel string) (string, bool, bool) {
	for _, m := range list {
		if m.ID == clientModel {
			return m.ID, true, true
		}
	}
	canon := CanonicalModelID(clientModel)
	if canon == "" {
		return "", false, false
	}
	if id, found := matchCanonicalInList(list, clientModel, canon); found {
		return id, false, true
	}
	return "", false, false
}

// MatchEmbeddingModel resolves clientModel against p's embedding catalog using exact,
// case-insensitive, then canonical (CanonicalModelID) matching.
func (c *Catalog) MatchEmbeddingModel(ctx context.Context, p *Provider, clientModel string) (upstreamID string, exact bool, ok bool) {
	return matchAux(c.EmbeddingModels(ctx, p), clientModel)
}

// parseEmbeddingModels extracts embedding-capable models from a provider `/models` response body.
func parseEmbeddingModels(providerID string, proto ir.Protocol, freeOnly bool, body []byte) []ModelInfo {
	if proto == ir.ProtoGemini {
		var gemResp struct {
			Models []struct {
				Name                       string   `json:"name"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"`
			} `json:"models"`
		}
		if json.Unmarshal(body, &gemResp) != nil {
			return nil
		}
		var out []ModelInfo
		for _, m := range gemResp.Models {
			id := strings.TrimPrefix(strings.TrimSpace(m.Name), "models/")
			if id == "" {
				continue
			}
			if !supportsEmbedContent(m.SupportedGenerationMethods) && !IsEmbeddingModel(id) {
				continue
			}
			out = append(out, ModelInfo{ID: id, ProviderID: providerID, OwnedBy: "google"})
		}
		return out
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
	var out []ModelInfo
	for _, m := range oaiResp.Data {
		if m.ID == "" || !IsEmbeddingModel(m.ID) {
			continue
		}
		if freeOnly && !isCatalogItemFree(m) {
			continue
		}
		owned := m.OwnedBy
		if owned == "" {
			owned = providerID
		}
		out = append(out, ModelInfo{ID: m.ID, ProviderID: providerID, OwnedBy: owned})
	}
	for _, m := range oaiResp.Models {
		id := m.ID
		if id == "" {
			id = m.Name
		}
		id = strings.TrimPrefix(strings.TrimSpace(id), "models/")
		if id == "" || !IsEmbeddingModel(id) {
			continue
		}
		out = append(out, ModelInfo{ID: id, ProviderID: providerID, OwnedBy: providerID})
	}
	return out
}

func supportsEmbedContent(methods []string) bool {
	for _, m := range methods {
		if m == "embedContent" || m == "batchEmbedContents" {
			return true
		}
	}
	return false
}

// parseRerankModels extracts rerank models from an OpenAI-style `/models` response body.
func parseRerankModels(providerID string, body []byte) []ModelInfo {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return nil
	}
	var out []ModelInfo
	for _, m := range resp.Data {
		if IsRerankModel(m.ID) {
			out = append(out, ModelInfo{ID: m.ID, ProviderID: providerID})
		}
	}
	return out
}

// SupportsRerank reports whether p has an OpenAI-style base URL for `/rerank`.
func (p *Provider) SupportsRerank() bool {
	return p != nil && p.EndpointFor(ir.ProtoOpenAIChat) != ""
}
