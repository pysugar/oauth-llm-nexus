package util

import "fmt"

// DefaultLogMaxLen is the default maximum length for truncated log output (1KB)
// Full content is available via Monitor's request/response capture
const DefaultLogMaxLen = 1024

// TruncateLog truncates long strings for verbose logging.
// This helps control log file growth while maintaining diagnostics capability.
// Full request/response content can be viewed via the /monitor/history page.
func TruncateLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + fmt.Sprintf("... [truncated, %d bytes total]", len(s))
}

// TruncateBytes is a convenience wrapper for TruncateLog that accepts []byte
// and uses DefaultLogMaxLen. This simplifies common logging patterns.
func TruncateBytes(b []byte) string {
	return TruncateLog(string(b), DefaultLogMaxLen)
}
