package ir

import "strings"

// NormalizeEffort canonicalizes a reasoning effort string across protocols.
func NormalizeEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "", "unset":
		return ""
	case "none", "off", "disable", "disabled":
		return "none"
	case "minimal":
		return "minimal"
	case "low":
		return "low"
	case "medium", "auto", "dynamic":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "xhigh"
	case "max":
		return "max"
	default:
		return strings.ToLower(strings.TrimSpace(effort))
	}
}

// EffortToBudget converts a Reasoning effort level to a token budget estimate.
func EffortToBudget(effort string) int {
	switch NormalizeEffort(effort) {
	case "none":
		return 0
	case "minimal":
		return 1024
	case "low":
		return 2048
	case "medium":
		return 8192
	case "high":
		return 16384
	case "xhigh":
		return 24576
	case "max":
		return 32768
	default:
		return 0
	}
}

// BudgetToEffort converts a numeric thinking token budget into a normalized effort level.
func BudgetToEffort(budget int) string {
	switch {
	case budget == 0:
		return "none"
	case budget < 0:
		return "medium"
	case budget <= 1536:
		return "minimal"
	case budget <= 4096:
		return "low"
	case budget <= 12288:
		return "medium"
	case budget <= 20480:
		return "high"
	default:
		return "xhigh"
	}
}

// EffectiveEffort returns the normalized effort string implied by r.
func EffectiveEffort(r Reasoning) string {
	if eff := NormalizeEffort(r.Effort); eff != "" {
		return eff
	}
	if r.Raw || r.Budget != 0 {
		return BudgetToEffort(r.Budget)
	}
	return ""
}

// EffectiveBudget returns the concrete thinking token budget implied by r.
func EffectiveBudget(r Reasoning) int {
	if r.Budget != 0 {
		return r.Budget
	}
	return EffortToBudget(r.Effort)
}
