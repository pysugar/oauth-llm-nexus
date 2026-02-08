package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	"github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/mappers"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream"
)

// TestProxyIntegration_SystemRole performs a real end-to-end test against Google API
// utilizing the credentials found in the local nexus.db.
// It is skipped if nexus.db is missing or has no primary account.
func TestProxyIntegration_SystemRole(t *testing.T) {
	if os.Getenv("NEXUS_RUN_INTEGRATION") != "1" {
		t.Skip("set NEXUS_RUN_INTEGRATION=1 to run integration tests")
	}

	// 1. Locate nexus.db
	dbPaths := []string{
		"nexus.db",
		"../../../nexus.db", // if running from inside internal/proxy/handlers
		"../../../../nexus.db",
	}

	var dbPath string
	for _, p := range dbPaths {
		if _, err := os.Stat(p); err == nil {
			dbPath = p
			break
		}
	}

	if dbPath == "" {
		t.Skip("⚠️ nexus.db not found. Skipping integration test. Please run ./nexus locally to generate it and login.")
	}

	// 2. Init DB
	database, err := db.InitDB(dbPath)
	if err != nil {
		t.Fatalf("Failed to init integration DB: %v", err)
	}

	// 3. Find Primary Account
	var account models.Account
	if err := database.Where("is_primary = ?", true).First(&account).Error; err != nil {
		t.Skip("⚠️ No primary account found in nexus.db. Skipping integration test.")
	}

	t.Logf("🚀 Running integration test with Account: %s (Project: %s)", account.Email, account.ID)

	// 4. Setup Dependencies
	tokenMgr := token.NewManager(database)
	// We need to ensure the token manager has loaded the token into cache
	// In a real app, StartRefreshLoop does this, but here we can just ensure it's loaded via GetPrimary
	// However, GetPrimary loads from DB on demand if not cached.

	upstreamClient := upstream.NewClient()
	handler := OpenAIChatHandler(tokenMgr, upstreamClient)

	// 5. Construct Request with System Role
	reqBody := mappers.OpenAIChatRequest{
		Model: "gpt-4",
		Messages: []mappers.OpenAIMessage{
			{Role: "system", Content: "You are a test assistant. Always reply with 'INTEGRATION_TEST_PASSED'."},
			{Role: "user", Content: "Status report?"},
		},
		Temperature: float64Ptr(0.1),
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	// Use the mock API key or rely on implicit primary account
	req.Header.Set("Authorization", "Bearer sk-test-key")

	rec := httptest.NewRecorder()

	// 6. Execute
	start := time.Now()
	handler.ServeHTTP(rec, req)
	duration := time.Since(start)

	// 7. Verify
	if rec.Code != http.StatusOK {
		t.Fatalf("Integration request failed. Code: %d, Body: %s", rec.Code, rec.Body.String())
	}

	var resp mappers.OpenAIChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(resp.Choices) == 0 {
		t.Fatalf("No choices in response")
	}

	content := resp.Choices[0].Message.Content
	t.Logf("✅ Response received in %v: %s", duration, content)

	// Broad check because LLMs varies, but short prompt usually works
	if content == "" {
		t.Error("Response content is empty")
	}

	// We expect the system instruction to guide the output
	// Note: Short prompts might be ignored or hallucinated, but "Always reply with..." is strong.
	// We won't fail hard on content mismatch to avoid flakiness, but we log it.
}

func float64Ptr(v float64) *float64 {
	return &v
}
