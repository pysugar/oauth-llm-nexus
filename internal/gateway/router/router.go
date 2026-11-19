package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
	"github.com/pysugar/oauth-llm-nexus/internal/gateway/provider"
	"gorm.io/gorm"
)

// Candidate represents a resolved upstream backend attempt target for a request.
type Candidate struct {
	Provider      *provider.Provider
	UpstreamModel string
	Source        string // "header", "prefix", "db", "catalog"
	Priority      int
	Cooling       bool // provider is in cooldown: tried only after every healthy candidate
}

// Plan holds ordered gateway candidates and optional legacy OAuth delegation info.
type Plan struct {
	ClientModel            string
	Strategy               string
	Forced                 bool
	Candidates             []Candidate
	LegacyProvider         string
	LegacyModel            string
	LegacyFallbackProvider string
	LegacyFallbackModel    string
	SkippedReasons         []string
}

// Router resolves incoming model identifiers into ordered upstream Candidates and failover Plans.
type Router struct {
	Registry        *provider.Registry
	Cooldown        *Cooldown
	DB              *gorm.DB
	LegacyAvailable func(providerID string) bool
}

// New constructs a Router backed by reg, cd, and optional sqlite database.
func New(reg *provider.Registry, cd *Cooldown, database *gorm.DB) *Router {
	if cd == nil {
		cd = NewCooldown()
	}
	return &Router{
		Registry: reg,
		Cooldown: cd,
		DB:       database,
	}
}

// IsLegacyAvailable reports whether legacy OAuth/AI-Studio providerID has usable runtime credentials.
func (r *Router) IsLegacyAvailable(providerID string) bool {
	norm := db.NormalizeProvider(providerID)
	if r != nil && r.LegacyAvailable != nil {
		return r.LegacyAvailable(norm)
	}
	if norm == "google" {
		return r != nil && r.DB != nil && db.HasActiveOAuthAccounts(r.DB)
	}
	return isLegacyOAuthProvider(norm)
}

// Resolve builds an execution Plan for clientModel, honoring explicit header/prefix overrides,
// DB route priorities + policy ("failover" | "pinned"), and exact catalog model discovery.
func (r *Router) Resolve(ctx context.Context, clientModel, headerProvider string) (*Plan, *ir.GatewayError) {
	rawModel := strings.TrimSpace(clientModel)
	if rawModel == "" {
		return nil, ir.NewError(400, "missing_model", "model is required")
	}

	overrideProvider := strings.ToLower(strings.TrimSpace(headerProvider))
	overrideSource := "header"
	bareModel := rawModel

	if overrideProvider == "" {
		if pfx, rest, ok := r.splitProviderPrefix(rawModel); ok && r.shouldTreatAsProviderPrefix(ctx, rawModel, pfx, rest) {
			overrideProvider = pfx
			bareModel = rest
			overrideSource = "prefix"
		}
	}

	// 1. Explicit provider override via X-Nexus-Provider or <provider>/<model>.
	if overrideProvider != "" {
		return r.resolveExplicit(ctx, rawModel, bareModel, overrideProvider, overrideSource)
	}

	// 2. Explicit DB ModelRoute candidates sorted by (priority ASC, id ASC).
	routes := db.GetModelCandidates(rawModel)
	strategy := db.GetModelPolicy(rawModel)
	plan := &Plan{
		ClientModel: rawModel,
		Strategy:    strategy,
	}

	if len(routes) > 0 {
		// If the highest-priority route targets a legacy OAuth provider, delegate directly
		// only when it is pinned or available (and no available gateway candidate follows it).
		firstProv := db.NormalizeProvider(routes[0].TargetProvider)
		if isLegacyOAuthProvider(firstProv) && !r.Registry.Has(firstProv) {
			legacyReady := r.IsLegacyAvailable(firstProv)
			if strategy == models.StrategyPinned || (legacyReady && !r.hasAnyAvailableGatewayCandidate(routes[1:])) {
				plan.LegacyProvider = firstProv
				plan.LegacyModel = routes[0].TargetModel
				return plan, nil
			}
		}

		var availableCandidates []Candidate
		var coolingCandidates []Candidate

		for _, route := range routes {
			provID := db.NormalizeProvider(route.TargetProvider)
			if isLegacyOAuthProvider(provID) && !r.Registry.Has(provID) {
				if r.IsLegacyAvailable(provID) {
					if plan.LegacyFallbackProvider == "" {
						plan.LegacyFallbackProvider = provID
						plan.LegacyFallbackModel = route.TargetModel
					}
				} else {
					plan.SkippedReasons = append(plan.SkippedReasons, provID+": no active OAuth/legacy credentials")
				}
				continue
			}

			p, ok := r.Registry.Get(provID)
			if !ok {
				plan.SkippedReasons = append(plan.SkippedReasons, provID+": not registered")
				continue
			}
			if !p.Available() {
				plan.SkippedReasons = append(plan.SkippedReasons, provID+": disabled or missing credentials")
				continue
			}

			targetModel := strings.TrimSpace(route.TargetModel)
			if targetModel == "" {
				targetModel = p.MapModel(rawModel)
			}
			cand := Candidate{
				Provider:      p,
				UpstreamModel: targetModel,
				Source:        "db",
				Priority:      route.Priority,
			}

			if strategy == models.StrategyPinned {
				// Pin exclusively to the primary configured candidate (priority 0).
				plan.Forced = true
				plan.Candidates = []Candidate{cand}
				return plan, nil
			}

			if cooling, rem, reason := r.Cooldown.IsCoolingDown(p.ID); cooling {
				plan.SkippedReasons = append(plan.SkippedReasons,
					fmt.Sprintf("%s: cooling down (%ds, %s)", p.ID, int(rem.Seconds()), reason))
				cand.Cooling = true
				coolingCandidates = append(coolingCandidates, cand)
				continue
			}
			availableCandidates = append(availableCandidates, cand)
		}

		if len(availableCandidates) > 0 {
			// Cooling backends stay as a last resort behind every healthy one.
			plan.Candidates = append(availableCandidates, coolingCandidates...)
			return plan, nil
		}
		if len(coolingCandidates) > 0 {
			return nil, ir.NewError(503, "all_backends_cooling_down",
				fmt.Sprintf("all configured backends for %q are temporarily in cooldown (%s)", rawModel, strings.Join(plan.SkippedReasons, "; ")))
		}
		if plan.LegacyFallbackProvider != "" {
			plan.LegacyProvider = plan.LegacyFallbackProvider
			plan.LegacyModel = plan.LegacyFallbackModel
			return plan, nil
		}
		// Only stop here if DB explicitly named gateway backends that failed readiness;
		// if only inactive legacy routes (e.g. seeded google OAuth without accounts) were skipped,
		// fall through to Catalog discovery so configured API backends can still serve the model.
		if len(plan.SkippedReasons) > 0 && r.hasAnyConfiguredGatewayRoute(routes) {
			return nil, ir.NewError(503, "no_available_backend",
				fmt.Sprintf("no configured backend available for %q (%s)", rawModel, strings.Join(plan.SkippedReasons, "; ")))
		}
	}

	// 3. Implicit catalog discovery across available gateway providers in providers.yaml order.
	// Exact model ID matches are preferred; canonical normalization (CanonicalModelID) kicks in
	// when the caller passes an un-namespaced alias such as "deepseek-r1".
	if r.Registry != nil && r.Registry.Catalog() != nil {
		resolved, ge := r.resolveCatalogCandidates(ctx, rawModel, plan)
		if ge != nil {
			return nil, ge
		}
		if resolved != nil {
			return resolved, nil
		}
	}

	// 4. Legacy OAuth fallback when active OAuth accounts exist in DB (e.g., native Gemini OAuth models).
	if r.IsLegacyAvailable("google") {
		plan.LegacyProvider = "google"
		plan.LegacyModel = rawModel
		return plan, nil
	}
	if len(plan.SkippedReasons) > 0 {
		return nil, ir.NewError(503, "no_available_backend",
			fmt.Sprintf("no configured backend available for %q (%s)", rawModel, strings.Join(plan.SkippedReasons, "; ")))
	}

	return nil, ir.NewError(404, "model_not_found",
		fmt.Sprintf("model %q was not found in configured routes or discovered provider catalogs", rawModel))
}

func (r *Router) resolveCatalogCandidates(ctx context.Context, rawModel string, plan *Plan) (*Plan, *ir.GatewayError) {
	cat := r.Registry.Catalog()
	providers := r.Registry.Available()

	var exactAvail, exactCool []Candidate
	for _, p := range providers {
		if !cat.HasExactModel(ctx, p, rawModel) {
			continue
		}
		cand := Candidate{
			Provider:      p,
			UpstreamModel: p.MapModel(rawModel),
			Source:        "catalog",
		}
		if coolingFlag, rem, reason := r.Cooldown.IsCoolingDown(p.ID); coolingFlag {
			plan.SkippedReasons = append(plan.SkippedReasons,
				fmt.Sprintf("%s: cooling down (%ds, %s)", p.ID, int(rem.Seconds()), reason))
			cand.Cooling = true
			exactCool = append(exactCool, cand)
			continue
		}
		exactAvail = append(exactAvail, cand)
	}
	if len(exactAvail) > 0 {
		plan.Candidates = append(exactAvail, exactCool...)
		return plan, nil
	}
	if len(exactCool) > 0 {
		return nil, ir.NewError(503, "all_backends_cooling_down",
			fmt.Sprintf("all matched backends for %q are cooling down (%s)", rawModel, strings.Join(plan.SkippedReasons, "; ")))
	}

	var canonAvail, canonCool []Candidate
	for _, p := range providers {
		upstreamID, ok := cat.MatchCanonicalModel(ctx, p, rawModel)
		if !ok {
			continue
		}
		cand := Candidate{
			Provider:      p,
			UpstreamModel: p.MapModel(upstreamID),
			Source:        "catalog",
		}
		if coolingFlag, rem, reason := r.Cooldown.IsCoolingDown(p.ID); coolingFlag {
			plan.SkippedReasons = append(plan.SkippedReasons,
				fmt.Sprintf("%s: cooling down (%ds, %s)", p.ID, int(rem.Seconds()), reason))
			cand.Cooling = true
			canonCool = append(canonCool, cand)
			continue
		}
		canonAvail = append(canonAvail, cand)
	}
	if len(canonAvail) > 0 {
		plan.Candidates = append(canonAvail, canonCool...)
		return plan, nil
	}
	if len(canonCool) > 0 {
		return nil, ir.NewError(503, "all_backends_cooling_down",
			fmt.Sprintf("all canonical backends for %q are cooling down (%s)", rawModel, strings.Join(plan.SkippedReasons, "; ")))
	}
	return nil, nil
}

func (r *Router) resolveExplicit(ctx context.Context, rawModel, bareModel, provID, source string) (*Plan, *ir.GatewayError) {
	if isLegacyOAuthProvider(provID) && (r.Registry == nil || !r.Registry.Has(provID)) {
		target := db.GetModelRoute(rawModel, provID)
		if target == "" {
			target = bareModel
		}
		return &Plan{
			ClientModel:    rawModel,
			Strategy:       models.StrategyPinned,
			Forced:         true,
			LegacyProvider: provID,
			LegacyModel:    target,
		}, nil
	}

	p, ok := r.Registry.Get(provID)
	if !ok {
		return nil, ir.NewError(404, "unknown_provider", fmt.Sprintf("unknown provider %q", provID))
	}
	if !p.Available() {
		return nil, ir.NewError(503, "provider_unavailable",
			fmt.Sprintf("provider %q is disabled or has no credentials configured", p.ID))
	}

	targetModel := ""
	if dbTarget := db.GetModelRoute(rawModel, p.ID); dbTarget != "" {
		targetModel = dbTarget
	} else if bareModel != rawModel && db.GetModelRoute(bareModel, p.ID) != "" {
		targetModel = db.GetModelRoute(bareModel, p.ID)
	} else {
		candidateModel := bareModel
		if bareModel == rawModel {
			if pfx, rest, hasPfx := r.splitProviderPrefix(rawModel); hasPfx && pfx == p.ID && r.Registry.Catalog() != nil {
				if !r.Registry.Catalog().HasExactModel(ctx, p, rawModel) && r.Registry.Catalog().HasExactModel(ctx, p, rest) {
					candidateModel = rest
				}
			}
		}
		targetModel = p.MapModel(candidateModel)
	}

	return &Plan{
		ClientModel: rawModel,
		Strategy:    models.StrategyPinned,
		Forced:      true,
		Candidates: []Candidate{{
			Provider:      p,
			UpstreamModel: targetModel,
			Source:        source,
		}},
	}, nil
}

// shouldTreatAsProviderPrefix disambiguates explicit `<provider>/<model>` routing prefixes
// from upstream vendor-namespaced model IDs (such as `nvidia/nemotron-...`, `nvidia/...:free`,
// `google/gemma-...`, `openai/gpt-oss-...`, or `deepseek/...`).
func (r *Router) shouldTreatAsProviderPrefix(ctx context.Context, rawModel, pfx, rest string) bool {
	if len(db.GetModelCandidates(rawModel)) > 0 {
		return false
	}

	if isLegacyOAuthProvider(pfx) && (r.Registry == nil || !r.Registry.Has(pfx)) {
		if db.GetModelRoute(rawModel, pfx) != "" || db.GetModelRoute(rest, pfx) != "" {
			return true
		}
		if r.hasCatalogModel(ctx, rawModel) {
			return false
		}
		if r.DB == nil || !db.HasActiveOAuthAccounts(r.DB) {
			return false
		}
		return true
	}

	if r.Registry == nil {
		return false
	}
	p, ok := r.Registry.Get(pfx)
	if !ok {
		return false
	}
	if db.GetModelRoute(rawModel, p.ID) != "" || db.GetModelRoute(rest, p.ID) != "" {
		return true
	}
	if !p.Available() {
		if r.hasCatalogModel(ctx, rawModel) {
			return false
		}
		return true
	}
	if r.Registry.Catalog() != nil {
		if r.Registry.Catalog().HasExactModel(ctx, p, rawModel) {
			return false
		}
		if r.Registry.Catalog().HasExactModel(ctx, p, rest) {
			return true
		}
		if r.hasCatalogModel(ctx, rawModel) {
			return false
		}
	}
	return true
}

func (r *Router) hasCatalogModel(ctx context.Context, modelID string) bool {
	if r.Registry == nil || r.Registry.Catalog() == nil {
		return false
	}
	for _, p := range r.Registry.Available() {
		if r.Registry.Catalog().HasExactModel(ctx, p, modelID) {
			return true
		}
	}
	return false
}

func (r *Router) splitProviderPrefix(model string) (providerID, bareModel string, ok bool) {
	idx := strings.IndexByte(model, '/')
	if idx <= 0 || idx >= len(model)-1 {
		return "", "", false
	}
	prefix := strings.ToLower(strings.TrimSpace(model[:idx]))
	rest := strings.TrimSpace(model[idx+1:])
	if rest == "" {
		return "", "", false
	}
	if (r.Registry != nil && r.Registry.Has(prefix)) || isLegacyOAuthProvider(prefix) {
		return prefix, rest, true
	}
	return "", "", false
}

func (r *Router) hasAnyAvailableGatewayCandidate(routes []models.ModelRoute) bool {
	if r.Registry == nil {
		return false
	}
	for _, route := range routes {
		p, ok := r.Registry.Get(db.NormalizeProvider(route.TargetProvider))
		if ok && p.Available() {
			return true
		}
	}
	return false
}

func (r *Router) hasAnyConfiguredGatewayRoute(routes []models.ModelRoute) bool {
	for _, route := range routes {
		provID := db.NormalizeProvider(route.TargetProvider)
		if !isLegacyOAuthProvider(provID) || (r.Registry != nil && r.Registry.Has(provID)) {
			return true
		}
	}
	return false
}

func isLegacyOAuthProvider(id string) bool {
	switch id {
	case "google", "codex", "vertex":
		return true
	default:
		return false
	}
}
