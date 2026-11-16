package db

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.AutoMigrate(&models.ModelRoute{}, &models.ModelPolicy{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

func TestResolveModelWithProvider_DeterministicOrder(t *testing.T) {
	db := newTestDB(t)

	// First active row (id ASC) should win for duplicated client_model.
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
	if provider != "google" || target != "gemini-3-flash" {
		t.Fatalf("expected first-row google mapping, got provider=%s target=%s", provider, target)
	}
}

func TestResolveModelWithProviderForProtocol_RejectsIncompatibleProvider(t *testing.T) {
	db := newTestDB(t)
	if err := db.Create(&models.ModelRoute{
		ClientModel:    "gemini-3-flash-preview",
		TargetProvider: "vertex",
		TargetModel:    "gemini-3-flash-preview",
		IsActive:       true,
	}).Error; err != nil {
		t.Fatalf("create route: %v", err)
	}

	loadModelRouteCache(db)

	if _, _, err := ResolveModelWithProviderForProtocol("gemini-3-flash-preview", string(ProtocolOpenAI)); err == nil {
		t.Fatal("expected openai protocol to reject vertex provider")
	}
}

func TestMultiBackendPriorityAndPolicySwitch(t *testing.T) {
	db := newTestDB(t)

	_ = db.Create(&models.ModelRoute{
		ClientModel:    "deepseek-r1",
		TargetProvider: "deepseek",
		TargetModel:    "deepseek-reasoner",
		Priority:       10,
		IsActive:       true,
	}).Error
	_ = db.Create(&models.ModelRoute{
		ClientModel:    "deepseek-r1",
		TargetProvider: "nvidia",
		TargetModel:    "deepseek-ai/deepseek-r1",
		Priority:       1,
		IsActive:       true,
	}).Error
	loadModelRouteCache(db)

	cands := GetModelCandidates("deepseek-r1")
	if len(cands) != 2 || cands[0].TargetProvider != "nvidia" || cands[1].TargetProvider != "deepseek" {
		t.Fatalf("expected nvidia (priority 1) before deepseek (priority 10), got %+v", cands)
	}

	if err := SelectModelProvider(db, "deepseek-r1", "deepseek", ""); err != nil {
		t.Fatalf("SelectModelProvider failed: %v", err)
	}
	cands = GetModelCandidates("deepseek-r1")
	if cands[0].TargetProvider != "deepseek" || cands[0].Priority != 0 || cands[1].TargetProvider != "nvidia" {
		t.Fatalf("expected deepseek promoted to priority 0, got %+v", cands)
	}

	if err := SelectModelProvider(db, "deepseek-r1", "amd", "amd/deepseek-r1"); err != nil {
		t.Fatalf("SelectModelProvider insert failed: %v", err)
	}
	cands = GetModelCandidates("deepseek-r1")
	if len(cands) != 3 || cands[0].TargetProvider != "amd" || cands[0].TargetModel != "amd/deepseek-r1" {
		t.Fatalf("expected amd inserted at priority 0, got %+v", cands)
	}

	if err := SetModelPolicy(db, "deepseek-r1", models.StrategyPinned); err != nil {
		t.Fatalf("SetModelPolicy failed: %v", err)
	}
	if got := GetModelPolicy("deepseek-r1"); got != models.StrategyPinned {
		t.Fatalf("expected pinned strategy, got %q", got)
	}
}
