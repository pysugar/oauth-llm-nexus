package router

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/gateway/ir"
)

// FailureAction specifies how the gateway should react to a failed upstream attempt.
type FailureAction struct {
	RotateKey bool // Attempt another key from the provider's KeyRing before moving on
	Failover  bool // Fall back to the next candidate backend if available
	Cooldown  bool // Place this provider into backoff cooldown
}

// RateLimitHints holds parsed backoff and exhaustion signals from upstream HTTP headers.
type RateLimitHints struct {
	RetryAfter           time.Duration
	ProactivelyExhausted bool
}

const (
	minHeaderCooldown = 1 * time.Second
	maxHeaderCooldown = 15 * time.Minute
)

// ParseRateLimitHeaders extracts explicit reset windows (Retry-After, x-ratelimit-reset-*)
// and remaining-quota exhaustion flags from an upstream HTTP response header map.
func ParseRateLimitHeaders(h http.Header, now time.Time) RateLimitHints {
	var hints RateLimitHints
	if len(h) == 0 {
		return hints
	}

	if rem := strings.TrimSpace(h.Get("x-ratelimit-remaining-requests")); rem == "0" {
		hints.ProactivelyExhausted = true
	}

	if msRaw := strings.TrimSpace(h.Get("Retry-After-Ms")); msRaw != "" {
		if ms, err := strconv.ParseInt(msRaw, 10, 64); err == nil && ms > 0 {
			hints.RetryAfter = clampHeaderCooldown(time.Duration(ms) * time.Millisecond)
			return hints
		}
	}

	if ra := strings.TrimSpace(h.Get("Retry-After")); ra != "" {
		if d := parseRetryAfterValue(ra, now); d > 0 {
			hints.RetryAfter = clampHeaderCooldown(d)
			return hints
		}
	}

	for _, hdr := range []string{"x-ratelimit-reset-requests", "x-ratelimit-reset-tokens", "x-ratelimit-reset"} {
		if raw := strings.TrimSpace(h.Get(hdr)); raw != "" {
			if d := parseResetValue(raw, now); d > 0 {
				hints.RetryAfter = clampHeaderCooldown(d)
				return hints
			}
		}
	}
	return hints
}

func parseRetryAfterValue(raw string, now time.Time) time.Duration {
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if ts, err := http.ParseTime(raw); err == nil {
		if d := ts.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func parseResetValue(raw string, now time.Time) time.Duration {
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if num, err := strconv.ParseInt(raw, 10, 64); err == nil && num > 0 {
		if num > 1_000_000_000_000 {
			// Epoch milliseconds
			if d := time.UnixMilli(num).Sub(now); d > 0 {
				return d
			}
			return 0
		}
		if num > 1_000_000_000 {
			// Epoch seconds
			if d := time.Unix(num, 0).Sub(now); d > 0 {
				return d
			}
			return 0
		}
		// Relative seconds
		return time.Duration(num) * time.Second
	}
	return 0
}

func clampHeaderCooldown(d time.Duration) time.Duration {
	if d < minHeaderCooldown {
		return minHeaderCooldown
	}
	if d > maxHeaderCooldown {
		return maxHeaderCooldown
	}
	return d
}

// ClassifyFailure evaluates an HTTP status code or transport error to decide retry, key rotation, and failover.
func ClassifyFailure(status int, err error) FailureAction {
	if err != nil {
		return FailureAction{
			RotateKey: false,
			Failover:  true,
			Cooldown:  true,
		}
	}
	switch {
	case status == 401 || status == 402 || status == 403:
		return FailureAction{
			RotateKey: true,
			Failover:  true,
			Cooldown:  true,
		}
	case status == 429:
		return FailureAction{
			RotateKey: true,
			Failover:  true,
			Cooldown:  true,
		}
	case status == 404 || status == 410:
		// Model missing or retired on one backend should fail over cleanly without poisoning the backend globally.
		return FailureAction{
			RotateKey: false,
			Failover:  true,
			Cooldown:  false,
		}
	case status == 503 || status == 529:
		// High-demand / overloaded upstream should immediately relay to sibling keys and failover backends.
		return FailureAction{
			RotateKey: true,
			Failover:  true,
			Cooldown:  true,
		}
	case status == 408 || status >= 500:
		return FailureAction{
			RotateKey: false,
			Failover:  true,
			Cooldown:  true,
		}
	default:
		// Client validation errors (400, 413, 422) surface immediately without failover.
		return FailureAction{}
	}
}

// ClassifyGatewayError inspects both HTTP status and structured upstream error fields
// (e.g., Google AI Studio's HTTP 400 API_KEY_INVALID) to determine key rotation, cooldown duration, and failover.
func ClassifyGatewayError(ge *ir.GatewayError) (FailureAction, time.Duration) {
	if ge == nil {
		return ClassifyFailure(0, nil), 0
	}
	if IsStreamQualityError(ge) {
		// The backend answered but the reply was unusable (cut short, empty, too slow to start):
		// try the next backend without blaming the key or cooling the provider.
		return FailureAction{Failover: true}, 0
	}
	if ge.HTTPStatus == 400 && isUpstreamKeyRejectedError(ge) {
		return FailureAction{
			RotateKey: true,
			Failover:  true,
			Cooldown:  true,
		}, 1 * time.Hour
	}
	if ge.HTTPStatus == 503 || ge.HTTPStatus == 529 {
		return FailureAction{
			RotateKey: true,
			Failover:  true,
			Cooldown:  true,
		}, 15 * time.Second
	}
	return ClassifyFailure(ge.HTTPStatus, nil), 0
}

func isUpstreamKeyRejectedError(ge *ir.GatewayError) bool {
	if ge == nil {
		return false
	}
	hay := strings.ToLower(ge.Code + " " + ge.Type + " " + ge.Message)
	for _, needle := range []string{
		"api_key_invalid",
		"api key not valid",
		"invalid api key",
		"key expired",
		"expired api key",
		"api key expired",
	} {
		if strings.Contains(hay, needle) {
			return true
		}
	}
	return false
}

// IsStreamQualityError reports gateway-detected reply defects that warrant failover only.
func IsStreamQualityError(ge *ir.GatewayError) bool {
	if ge == nil {
		return false
	}
	switch ge.Code {
	case "upstream_incomplete", "empty_reply", "upstream_finish_error", "first_token_timeout", "upstream_idle_timeout",
		"upstream_key_limit": // all keys over their spend caps: move on, the backend itself is fine
		return true
	}
	return false
}
