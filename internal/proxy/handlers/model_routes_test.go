package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
)

func newModelRoutesTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := database.AutoMigrate(&models.ModelRoute{}); err != nil {
		t.Fatalf("failed to migrate model routes: %v", err)
	}
	return database
}

func TestCreateModelRouteHandler_RejectsInvalidProviderForPrefix(t *testing.T) {
	database := newModelRoutesTestDB(t)

	body := `{"client_model":"gpt-4o","target_provider":"vertex","target_model":"gemini-3-flash-preview"}`
	req := httptest.NewRequest(http.MethodPost, "/api/model-routes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	CreateModelRouteHandler(database).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not allowed") {
		t.Fatalf("expected not-allowed error, got %s", rec.Body.String())
	}
}

func TestUpdateModelRouteHandler_RejectsInvalidProviderForPrefix(t *testing.T) {
	database := newModelRoutesTestDB(t)
	seed := models.ModelRoute{
		ClientModel:    "claude-opus-4-6-thinking",
		TargetProvider: "google",
		TargetModel:    "claude-opus-4-6-thinking",
		IsActive:       true,
	}
	if err := database.Create(&seed).Error; err != nil {
		t.Fatalf("failed to seed route: %v", err)
	}

	router := chi.NewRouter()
	router.Put("/api/model-routes/{id}", UpdateModelRouteHandler(database))

	body := `{"client_model":"claude-opus-4-6-thinking","target_provider":"codex","target_model":"claude-opus-4-6-thinking","is_active":true}`
	req := httptest.NewRequest(http.MethodPut, "/api/model-routes/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not allowed") {
		t.Fatalf("expected not-allowed error, got %s", rec.Body.String())
	}
}
