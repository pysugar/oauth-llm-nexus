package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

// ResolveEmbedding builds an execution Plan for an embeddings request.
//
// Header/prefix overrides and DB ModelRoute aliases resolve exactly like chat models; when those
// yield no gateway candidate, the per-provider embedding catalogs are scanned (exact matches first,
// then canonical matches). Legacy OAuth backends never serve embeddings. Candidates without an
// embeddings-capable endpoint are dropped so failover only crosses compatible backends.
func (r *Router) ResolveEmbedding(ctx context.Context, clientModel, headerProvider string) (*Plan, *ir.GatewayError) {
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
			return nil, ir.NewError(400, "embeddings_unsupported",
				fmt.Sprintf("provider %q does not support embeddings", plan.LegacyProvider))
		}
		if len(plan.Candidates) > 0 {
			kept := plan.Candidates[:0:0]
			for _, c := range plan.Candidates {
				if c.Source == "prefix" && r.Registry != nil && r.Registry.Catalog() != nil {
					// Chat catalogs exclude embedding models, so vendor-namespaced IDs such as
					// `nvidia/nv-embedqa-e5-v5` look like provider prefixes; keep the full ID
					// when the provider's embedding catalog advertises it verbatim.
					if id, isExact, ok := r.Registry.Catalog().MatchEmbeddingModel(ctx, c.Provider, rawModel); ok && isExact {
						c.UpstreamModel = c.Provider.MapModel(id)
					}
				}
				if c.Provider.SupportsEmbeddings() {
					kept = append(kept, c)
				} else {
					plan.SkippedReasons = append(plan.SkippedReasons, c.Provider.ID+": no embeddings endpoint")
				}
			}
			if len(kept) > 0 {
				plan.Candidates = kept
				return plan, nil
			}
			if plan.Forced {
				return nil, ir.NewError(400, "embeddings_unsupported",
					fmt.Sprintf("provider %q has no embeddings-capable endpoint", plan.Candidates[0].Provider.ID))
			}
		}
	}

	out := &Plan{ClientModel: rawModel, Strategy: "failover"}
	if plan != nil {
		out.Strategy = plan.Strategy
		out.SkippedReasons = plan.SkippedReasons
	}
	if r.Registry == nil || r.Registry.Catalog() == nil {
		return nil, embeddingNotFound(rawModel)
	}

	cat := r.Registry.Catalog()
	var exact, canonical, cooling []Candidate
	for _, p := range r.Registry.Available() {
		if !p.SupportsEmbeddings() {
			continue
		}
		upstreamID, isExact, ok := cat.MatchEmbeddingModel(ctx, p, rawModel)
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
			fmt.Sprintf("all embedding backends for %q are cooling down (%s)", rawModel, strings.Join(out.SkippedReasons, "; ")))
	}
	return nil, embeddingNotFound(rawModel)
}

func embeddingNotFound(model string) *ir.GatewayError {
	return ir.NewError(404, "model_not_found",
		fmt.Sprintf("embedding model %q was not found in configured routes or provider embedding catalogs "+
			"(add a model route or use the <provider>/<model> prefix)", model))
}
