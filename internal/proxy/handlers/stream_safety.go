package handlers

import (
	"crypto/sha256"
	"time"
)

// StreamSafetyChecker detects repeated chunks to prevent infinite loops.
// Reference: LiteLLM's CustomStreamWrapper.safety_checker
type StreamSafetyChecker struct {
	lastChunkHash [32]byte
	repeatCount   int
	maxRepeats    int
	lastChunkTime time.Time
	streamTimeout time.Duration
}

// NewStreamSafetyChecker creates a new stream safety checker.
// maxRepeats: maximum allowed consecutive identical chunks (default: 10)
// timeout: maximum time between chunks before considered stale (default: 5 min)
func NewStreamSafetyChecker() *StreamSafetyChecker {
	return &StreamSafetyChecker{
		maxRepeats:    10,
		streamTimeout: 5 * time.Minute,
	}
}

// CheckChunk validates a stream chunk for safety.
// Returns (abort, reason) - if abort is true, the stream should be terminated.
func (s *StreamSafetyChecker) CheckChunk(data []byte) (abort bool, reason string) {
	now := time.Now()

	// Timeout check: if too long since last chunk, stream may be stale
	if !s.lastChunkTime.IsZero() && now.Sub(s.lastChunkTime) > s.streamTimeout {
		return true, "stream timeout exceeded"
	}
	s.lastChunkTime = now

	// Skip empty chunks
	if len(data) == 0 {
		return false, ""
	}

	// Repeat detection: hash the chunk and compare
	hash := sha256.Sum256(data)
	if hash == s.lastChunkHash {
		s.repeatCount++
		if s.repeatCount >= s.maxRepeats {
			return true, "repeated chunk detected"
		}
	} else {
		s.repeatCount = 0
		s.lastChunkHash = hash
	}

	return false, ""
}

// Reset clears the checker state for reuse.
func (s *StreamSafetyChecker) Reset() {
	s.lastChunkHash = [32]byte{}
	s.repeatCount = 0
	s.lastChunkTime = time.Time{}
}
