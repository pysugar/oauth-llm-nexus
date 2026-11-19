package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
)

// ResolveEmbedding builds an execution Plan for an embeddings request.
//
// Header/prefix overrides and DB ModelRoute aliases resolve exactly like chat models; when those
// yield no gateway candidate, the per-provider embedding catalogs are scanned (exact matches first,
// then canonical matches). Legacy OAuth backends never serve embeddings. Candidates without an
// embeddings-capable endpoint are dropped so failover only crosses compatible backends.
func (r *Router) ResolveEmbedding(ctx context.Context, clientModel, headerProvider string) (*Plan, *ir.GatewayError) {
	return r.resolveAux(ctx, clientModel, headerProvider, embeddingKind)
}

// ResolveRerank is ResolveEmbedding for `/v1/rerank` models.
func (r *Router) ResolveRerank(ctx context.Context, clientModel, headerProvider string) (*Plan, *ir.GatewayError) {
	return r.resolveAux(ctx, clientModel, headerProvider, rerankKind)
}

// auxKind describes a non-chat API (embeddings, rerank) for resolveAux.
type auxKind struct {
	name     string
	supports func(*provider.Provider) bool
	match    func(*provider.Catalog, context.Context, *provider.Provider, string) (string, bool, bool)
}

var embeddingKind = auxKind{
	name:     "embedding",
	supports: (*provider.Provider).SupportsEmbeddings,
	match:    (*provider.Catalog).MatchEmbeddingModel,
}

var rerankKind = auxKind{
	name:     "rerank",
	supports: (*provider.Provider).SupportsRerank,
	match:    (*provider.Catalog).MatchRerankModel,
}

func (r *Router) resolveAux(ctx context.Context, clientModel, headerProvider string, kind auxKind) (*Plan, *ir.GatewayError) {
	rawModel := strings.TrimSpace(clientModel)
	if rawModel == "" {
		return nil, ir.NewError(400, "missing_model", "model is required")
	}

	plan, ge := r.Resolve(ctx, rawModel, headerProvider)
	if ge != nil && ge.Code != "model_not_found" {
		return nil, ge
	}
	if plan != nil {
		if plan.LegacyProvider != "" && plan.Forced {
			return nil, ir.NewError(400, kind.name+"_unsupported",
				fmt.Sprintf("provider %q does not support %s", plan.LegacyProvider, kind.name))
		}
		if len(plan.Candidates) > 0 {
			kept := plan.Candidates[:0:0]
			for _, c := range plan.Candidates {
				if c.Source == "prefix" && r.Registry != nil && r.Registry.Catalog() != nil {
					// Chat catalogs exclude embedding models, so vendor-namespaced IDs such as
					// `nvidia/nv-embedqa-e5-v5` look like provider prefixes; keep the full ID
					// when the provider's embedding catalog advertises it verbatim.
					if id, isExact, ok := kind.match(r.Registry.Catalog(), ctx, c.Provider, rawModel); ok && isExact {
						c.UpstreamModel = c.Provider.MapModel(id)
					}
				}
				if kind.supports(c.Provider) {
					kept = append(kept, c)
				} else {
					plan.SkippedReasons = append(plan.SkippedReasons, c.Provider.ID+": no "+kind.name+" endpoint")
				}
			}
			if len(kept) > 0 {
				plan.Candidates = kept
				return plan, nil
			}
			if plan.Forced {
				return nil, ir.NewError(400, kind.name+"_unsupported",
					fmt.Sprintf("provider %q has no %s-capable endpoint", plan.Candidates[0].Provider.ID, kind.name))
			}
		}
	}

	out := &Plan{ClientModel: rawModel, Strategy: "failover"}
	if plan != nil {
		out.Strategy = plan.Strategy
		out.SkippedReasons = plan.SkippedReasons
	}
	if r.Registry == nil || r.Registry.Catalog() == nil {
		return nil, auxNotFound(kind, rawModel)
	}

	cat := r.Registry.Catalog()
	var exact, canonical, cooling []Candidate
	for _, p := range r.Registry.Available() {
		if !kind.supports(p) {
			continue
		}
		upstreamID, isExact, ok := kind.match(cat, ctx, p, rawModel)
		if !ok {
			continue
		}
		cand := Candidate{Provider: p, UpstreamModel: p.MapModel(upstreamID), Source: "catalog"}
		if cool, rem, reason := r.Cooldown.IsCoolingDown(p.ID); cool {
			out.SkippedReasons = append(out.SkippedReasons,
				fmt.Sprintf("%s: cooling down (%ds, %s)", p.ID, int(rem.Seconds()), reason))
			cooling = append(cooling, cand)
			continue
		}
		if isExact {
			exact = append(exact, cand)
		} else {
			canonical = append(canonical, cand)
		}
	}
	out.Candidates = append(exact, canonical...)
	if len(out.Candidates) > 0 {
		return out, nil
	}
	if len(cooling) > 0 {
		return nil, ir.NewError(503, "all_backends_cooling_down",
			fmt.Sprintf("all %s backends for %q are cooling down (%s)", kind.name, rawModel, strings.Join(out.SkippedReasons, "; ")))
	}
	return nil, auxNotFound(kind, rawModel)
}

func auxNotFound(kind auxKind, model string) *ir.GatewayError {
	return ir.NewError(404, "model_not_found",
		fmt.Sprintf("%s model %q was not found in configured routes or provider %s catalogs "+
			"(add a model route or use the <provider>/<model> prefix)", kind.name, model, kind.name))
}
