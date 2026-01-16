package util

import "testing"

func TestTruncateLog_ShortString(t *testing.T) {
	input := "short log"
	result := TruncateLog(input, DefaultLogMaxLen)
	if result != input {
		t.Errorf("TruncateLog() should not truncate short strings, got %q", result)
	}
}

func TestTruncateLog_ExactLimit(t *testing.T) {
	input := "12345678901234567890" // 20 chars
	result := TruncateLog(input, 20)
	if result != input {
		t.Errorf("TruncateLog() should not truncate at exact limit, got %q", result)
	}
}

func TestTruncateLog_LongString(t *testing.T) {
	input := "1234567890abcdefghij" // 20 chars
	result := TruncateLog(input, 10)
	if result != "1234567890... [truncated, 20 bytes total]" {
		t.Errorf("TruncateLog() = %q, want \"1234567890... [truncated, 20 bytes total]\"", result)
	}
}

func TestTruncateLog_EmptyString(t *testing.T) {
	result := TruncateLog("", 10)
	if result != "" {
		t.Errorf("TruncateLog() should return empty for empty input, got %q", result)
	}
}

func TestTruncateBytes_ShortBytes(t *testing.T) {
	input := []byte("short")
	result := TruncateBytes(input)
	if result != "short" {
		t.Errorf("TruncateBytes() should not truncate short bytes, got %q", result)
	}
}

func TestTruncateBytes_LongBytes(t *testing.T) {
	// Create bytes longer than DefaultLogMaxLen (1024)
	input := make([]byte, 2000)
	for i := range input {
		input[i] = 'x'
	}
	result := TruncateBytes(input)
	if len(result) <= DefaultLogMaxLen {
		t.Errorf("TruncateBytes() result should be longer than maxLen due to suffix, got len=%d", len(result))
	}
	if result[:DefaultLogMaxLen] != string(input[:DefaultLogMaxLen]) {
		t.Error("TruncateBytes() should preserve first DefaultLogMaxLen bytes")
	}
}
