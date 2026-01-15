package handlers

import (
	"testing"
	"time"
)

func TestStreamSafetyChecker_RepeatedChunks(t *testing.T) {
	checker := NewStreamSafetyChecker()
	checker.maxRepeats = 3 // After 3 consecutive same chunks, abort

	data := []byte("test chunk data")

	// First chunk - sets the hash, repeatCount = 0
	abort, _ := checker.CheckChunk(data)
	if abort {
		t.Error("First chunk should not abort")
	}

	// Second identical chunk - repeatCount = 1
	abort, _ = checker.CheckChunk(data)
	if abort {
		t.Error("Second chunk should not abort")
	}

	// Third identical chunk - repeatCount = 2
	abort, _ = checker.CheckChunk(data)
	if abort {
		t.Error("Third chunk should not abort (need 3 repeats)")
	}

	// Fourth identical chunk - repeatCount = 3, reaches threshold
	abort, reason := checker.CheckChunk(data)
	if !abort {
		t.Error("Fourth identical chunk should abort (3 repeats)")
	}
	if reason != "repeated chunk detected" {
		t.Errorf("Expected 'repeated chunk detected', got %q", reason)
	}
}

func TestStreamSafetyChecker_DifferentChunks(t *testing.T) {
	checker := NewStreamSafetyChecker()
	checker.maxRepeats = 3

	// Different chunks should not trigger abort
	for i := 0; i < 10; i++ {
		data := []byte{byte(i)}
		abort, _ := checker.CheckChunk(data)
		if abort {
			t.Errorf("Different chunk %d should not abort", i)
		}
	}
}

func TestStreamSafetyChecker_Timeout(t *testing.T) {
	checker := NewStreamSafetyChecker()
	checker.streamTimeout = 10 * time.Millisecond

	// First chunk
	checker.CheckChunk([]byte("first"))

	// Wait for timeout
	time.Sleep(20 * time.Millisecond)

	// Next chunk should trigger timeout
	abort, reason := checker.CheckChunk([]byte("second"))
	if !abort {
		t.Error("Should abort after timeout")
	}
	if reason != "stream timeout exceeded" {
		t.Errorf("Expected 'stream timeout exceeded', got %q", reason)
	}
}

func TestStreamSafetyChecker_Reset(t *testing.T) {
	checker := NewStreamSafetyChecker()
	checker.maxRepeats = 2

	data := []byte("test")
	checker.CheckChunk(data)
	checker.CheckChunk(data) // Should trigger on next

	// Reset and check again
	checker.Reset()
	abort, _ := checker.CheckChunk(data)
	if abort {
		t.Error("After reset, first chunk should not abort")
	}
}

func TestStreamSafetyChecker_EmptyChunks(t *testing.T) {
	checker := NewStreamSafetyChecker()
	checker.maxRepeats = 2

	// Empty chunks should be skipped
	for i := 0; i < 10; i++ {
		abort, _ := checker.CheckChunk([]byte{})
		if abort {
			t.Error("Empty chunks should not abort")
		}
	}
}
