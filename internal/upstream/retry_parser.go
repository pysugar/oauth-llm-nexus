package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryInfo represents the structured error response from Google API for 429 errors
type RetryInfo struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type       string `json:"@type"`
			Reason     string `json:"reason"`
			Domain     string `json:"domain"`
			Metadata   map[string]string
			RetryDelay string `json:"retryDelay"` // e.g. "3.5s"
		} `json:"details"`
	} `json:"error"`
}

// ParseRetryDelay attempts to extract a retry duration from a 429 response.
// It checks standard Retry-After header first, then tries to parse the JSON body.
// Returns 0 if no retry information is found.
// NOTE: This consumes and restores the response body if it needs to read it.
func ParseRetryDelay(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}

	// 1. Check Standard Header
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
		// Try seconds
		if seconds, err := strconv.Atoi(retryAfter); err == nil {
			return time.Duration(seconds) * time.Second
		}
		// Try HTTP date
		if t, err := http.ParseTime(retryAfter); err == nil {
			return time.Until(t)
		}
	}

	// 2. Check JSON Body for Google-specific properties
	// We need to read the body and then restore it for potential upstream forwarding
	if resp.Body == nil {
		return 0
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0
	}
	// Restore body immediately for safety
	resp.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

	var errInfo RetryInfo
	if err := json.Unmarshal(bodyBytes, &errInfo); err != nil {
		return 0
	}

	for _, detail := range errInfo.Error.Details {
		// Look for retryDelay in details
		if detail.RetryDelay != "" {
			// Format is usually "3.5s"
			if d, err := time.ParseDuration(detail.RetryDelay); err == nil {
				return d
			}
		}
		// Also check metadata if present
		if detail.Metadata != nil {
			if delay, ok := detail.Metadata["retryDelay"]; ok {
				if d, err := time.ParseDuration(delay); err == nil {
					return d
				}
			}
		}
		// Check for specific reasons
		if detail.Reason == "rateLimitExceeded" || detail.Reason == "quotaExceeded" {
			// If we have a reason but no explicit delay, default to a reasonable backoff
			// But for now, returning 0 lets the caller decide default backoff
		}
	}

	return 0
}
