package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/auth/token"
	dbpkg "github.com/pysugar/oauth-llm-nexus/internal/db"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/proxy/monitor"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream/geminikey"
	"github.com/pysugar/oauth-llm-nexus/internal/upstream/vertexkey"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:test-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite memory db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("failed to get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if err := db.AutoMigrate(&models.Account{}, &models.Config{}, &models.ModelRoute{}, &models.RequestLog{}); err != nil {
		t.Fatalf("failed to migrate test db: %v", err)
	}
	return db
}

func waitForLogCount(pm *monitor.ProxyMonitor, expected int) []models.RequestLog {
	for i := 0; i < 40; i++ {
		logs := pm.GetLogs(100, 0)
		if len(logs) >= expected {
			return logs
		}
		time.Sleep(20 * time.Millisecond)
	}
	return pm.GetLogs(100, 0)
}

func TestVertexAIStudioProxyHandlerWithMonitor_LogsWhenEnabled(t *testing.T) {
	db := newTestDB(t)
	pm := monitor.NewProxyMonitor(db)
	pm.SetEnabled(true)

	client := &http.Client{
		Timeout: time.Minute,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
			}, nil
		}),
	}

	oldProvider := VertexAIStudioProvider
	VertexAIStudioProvider = vertexkey.NewProviderWithClient("server-key", "https://aiplatform.googleapis.com", time.Minute, client)
	defer func() { VertexAIStudioProvider = oldProvider }()

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/publishers/google/models/gemini-2.5-flash-lite:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	VertexAIStudioProxyHandlerWithMonitor(pm).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	logs := waitForLogCount(pm, 1)
	if len(logs) == 0 {
		t.Fatalf("expected at least one log entry")
	}
	if logs[0].Provider != "vertex" {
		t.Fatalf("expected provider=vertex, got %q", logs[0].Provider)
	}
	if logs[0].Model != "gemini-2.5-flash-lite" {
		t.Fatalf("expected parsed model, got %q", logs[0].Model)
	}
}

func TestVertexAIStudioProxyHandlerWithMonitor_NoLogsWhenDisabled(t *testing.T) {
	db := newTestDB(t)
	pm := monitor.NewProxyMonitor(db)
	pm.SetEnabled(false)

	client := &http.Client{
		Timeout: time.Minute,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
			}, nil
		}),
	}

	oldProvider := VertexAIStudioProvider
	VertexAIStudioProvider = vertexkey.NewProviderWithClient("server-key", "https://aiplatform.googleapis.com", time.Minute, client)
	defer func() { VertexAIStudioProvider = oldProvider }()

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/publishers/google/models/gemini-2.5-flash-lite:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	VertexAIStudioProxyHandlerWithMonitor(pm).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	if stats := pm.GetStats(); stats.TotalRequests != 0 {
		t.Fatalf("expected no monitor logs when disabled, got %+v", stats)
	}
}

func TestGeminiAIStudioProxyHandlerWithMonitor_LogsWhenEnabled(t *testing.T) {
	db := newTestDB(t)
	pm := monitor.NewProxyMonitor(db)
	pm.SetEnabled(true)

	client := &http.Client{
		Timeout: time.Minute,
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`)),
			}, nil
		}),
	}

	oldProvider := GeminiAIStudioProvider
	GeminiAIStudioProvider = geminikey.NewProviderWithClient("server-key", "https://generativelanguage.googleapis.com", time.Minute, client)
	defer func() { GeminiAIStudioProvider = oldProvider }()

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1beta/models/gemini-2.5-flash:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	GeminiAIStudioProxyHandlerWithMonitor(pm).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	logs := waitForLogCount(pm, 1)
	if len(logs) == 0 {
		t.Fatalf("expected at least one log entry")
	}
	if logs[0].Provider != "gemini" {
		t.Fatalf("expected provider=gemini, got %q", logs[0].Provider)
	}
	if logs[0].Model != "gemini-2.5-flash" {
		t.Fatalf("expected parsed model, got %q", logs[0].Model)
	}
}

func TestOpenAIChatHandlerWithMonitor_LogsCodexProvider(t *testing.T) {
	db := newTestDB(t)
	pm := monitor.NewProxyMonitor(db)
	pm.SetEnabled(true)

	err := dbpkg.CreateModelRoute(db, &models.ModelRoute{
		ClientModel:    "gpt-5.2-codex",
		TargetProvider: "codex",
		TargetModel:    "gpt-5.2-codex",
		IsActive:       true,
	})
	if err != nil {
		t.Fatalf("failed to seed model route: %v", err)
	}

	tokenMgr := token.NewManager(db)

	oldProvider := CodexProvider
	CodexProvider = &fakeCodexProvider{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}}\n\n",
			)),
		},
	}
	defer func() { CodexProvider = oldProvider }()

	payload := map[string]interface{}{
		"model": "gpt-5.2-codex",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "say hi"},
		},
		"stream": false,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	OpenAIChatHandlerWithMonitor(tokenMgr, nil, pm).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	logs := waitForLogCount(pm, 1)
	if len(logs) == 0 {
		t.Fatalf("expected at least one log entry")
	}
	if logs[0].Provider != "codex" {
		t.Fatalf("expected provider=codex, got %q", logs[0].Provider)
	}
	if logs[0].MappedModel != "gpt-5.2-codex" {
		t.Fatalf("expected mapped model gpt-5.2-codex, got %q", logs[0].MappedModel)
	}
}

func TestRequestLogsHistory_SearchByProvider(t *testing.T) {
	db := newTestDB(t)
	pm := monitor.NewProxyMonitor(db)
	pm.SetEnabled(true)

	pm.LogRequest(models.RequestLog{
		Method:   http.MethodPost,
		URL:      "/v1/chat/completions",
		Status:   http.StatusOK,
		Duration: 12,
		Provider: "codex",
		Model:    "gpt-5.2-codex",
	})
	pm.LogRequest(models.RequestLog{
		Method:   http.MethodPost,
		URL:      "/anthropic/v1/messages",
		Status:   http.StatusOK,
		Duration: 18,
		Provider: "google",
		Model:    "claude-sonnet-4-5",
	})

	_ = waitForLogCount(pm, 2)

	req := httptest.NewRequest(http.MethodGet, "/api/request-logs/history?q=codex", nil)
	rec := httptest.NewRecorder()
	GetRequestLogsHistoryHandler(pm).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var got struct {
		Logs []models.RequestLog `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to parse history response: %v body=%s", err, rec.Body.String())
	}
	if len(got.Logs) == 0 {
		t.Fatalf("expected provider search to return logs, got none")
	}
	if got.Logs[0].Provider != "codex" {
		t.Fatalf("expected first log provider=codex, got %q", got.Logs[0].Provider)
	}
}

func TestRequestLogsHistory_SearchGeminiProvider(t *testing.T) {
	db := newTestDB(t)
	pm := monitor.NewProxyMonitor(db)
	pm.SetEnabled(true)

	pm.LogRequest(models.RequestLog{
		Method:   http.MethodPost,
		URL:      "/v1beta/models/gemini-3-flash-preview:streamGenerateContent",
		Status:   http.StatusOK,
		Duration: 15,
		Provider: "gemini",
		Model:    "gemini-3-flash-preview",
	})

	_ = waitForLogCount(pm, 2)

	req := httptest.NewRequest(http.MethodGet, "/api/request-logs/history?q=gemini", nil)
	rec := httptest.NewRecorder()
	GetRequestLogsHistoryHandler(pm).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var got struct {
		Logs []models.RequestLog `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to parse history response: %v body=%s", err, rec.Body.String())
	}
	if len(got.Logs) == 0 {
		t.Fatalf("expected gemini logs, got 0")
	}
	for _, entry := range got.Logs {
		if entry.Provider != "gemini" {
			t.Fatalf("expected provider=gemini only, got %q", entry.Provider)
		}
	}
}

func TestClaudeMessagesHandlerWithMonitor_InvalidRouteUsesInvalidProvider(t *testing.T) {
	db := newTestDB(t)
	pm := monitor.NewProxyMonitor(db)
	pm.SetEnabled(true)

	if err := dbpkg.CreateModelRoute(db, &models.ModelRoute{
		ClientModel:    "claude-opus-4-6-thinking",
		TargetProvider: "codex",
		TargetModel:    "claude-opus-4-6-thinking",
		IsActive:       true,
	}); err != nil {
		t.Fatalf("failed to seed invalid anthopic route: %v", err)
	}

	tokenMgr := token.NewManager(db)
	body := `{
		"model":"claude-opus-4-6-thinking",
		"messages":[{"role":"user","content":"hello"}],
		"max_tokens":32
	}`
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	ClaudeMessagesHandlerWithMonitor(tokenMgr, nil, pm).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status 422, got %d body=%s", rec.Code, rec.Body.String())
	}

	logs := waitForLogCount(pm, 1)
	if len(logs) == 0 {
		t.Fatalf("expected at least one log entry")
	}
	if logs[0].Provider != "invalid" {
		t.Fatalf("expected provider=invalid, got %q", logs[0].Provider)
	}
	if !strings.Contains(logs[0].Error, "invalid model route") {
		t.Fatalf("expected invalid model route error, got %q", logs[0].Error)
	}
}
