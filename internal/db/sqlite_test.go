package db

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.AutoMigrate(&models.ModelRoute{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

func TestResolveModelWithProvider_DeterministicOrder(t *testing.T) {
	db := newTestDB(t)

	// Insert in reverse order to verify cache load ordering is deterministic.
	if err := db.Create(&models.ModelRoute{
		ClientModel:    "gpt-4",
		TargetProvider: "google",
		TargetModel:    "gemini-3-flash",
		IsActive:       true,
	}).Error; err != nil {
		t.Fatalf("create route 1: %v", err)
	}
	if err := db.Create(&models.ModelRoute{
		ClientModel:    "gpt-4",
		TargetProvider: "codex",
		TargetModel:    "gpt-5.2",
		IsActive:       true,
	}).Error; err != nil {
		t.Fatalf("create route 2: %v", err)
	}

	loadModelRouteCache(db)

	target, provider := ResolveModelWithProvider("gpt-4")
	if provider != "codex" || target != "gpt-5.2" {
		t.Fatalf("expected deterministic codex mapping, got provider=%s target=%s", provider, target)
	}
}
