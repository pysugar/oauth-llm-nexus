package logging

import (
	"context"
	"testing"
)

func TestGenerateRequestID(t *testing.T) {
	id := GenerateRequestID()
	if len(id) != 8 {
		t.Errorf("GenerateRequestID() length = %d, want 8", len(id))
	}

	// Verify uniqueness
	id2 := GenerateRequestID()
	if id == id2 {
		t.Errorf("GenerateRequestID() generated duplicate IDs: %s", id)
	}
}

func TestRequestIDContext(t *testing.T) {
	ctx := context.Background()
	id := "test1234"

	// Without ID
	if got := GetRequestID(ctx); got != "" {
		t.Errorf("GetRequestID(empty context) = %q, want empty string", got)
	}

	// With ID
	ctx = WithRequestID(ctx, id)
	if got := GetRequestID(ctx); got != id {
		t.Errorf("GetRequestID() = %q, want %q", got, id)
	}
}

func TestGenerateAndRetrieveRoundTrip(t *testing.T) {
	ctx := context.Background()
	id := GenerateRequestID()
	ctx = WithRequestID(ctx, id)

	if got := GetRequestID(ctx); got != id {
		t.Errorf("RoundTrip failed: generated %q, retrieved %q", id, got)
	}
}
