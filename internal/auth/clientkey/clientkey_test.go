package clientkey

import (
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&models.ClientKey{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return gdb
}

func TestCreateResolveStoresOnlyHash(t *testing.T) {
	gdb := openTestDB(t)
	if HasKeys(gdb) {
		t.Fatal("expected no keys on empty table")
	}

	row, plaintext, err := Create(gdb, Spec{
		Name:             "pkm-app",
		AllowedProviders: []string{" Ollama ", "gemini", "ollama", ""},
		AllowedModels:    []string{"notes-*", "gpt-4o", "gpt-4o"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(plaintext, KeyPrefix) || len(plaintext) != len(KeyPrefix)+48 {
		t.Fatalf("unexpected key format %q", plaintext)
	}
	if row.KeyHash != Hash(plaintext) || row.KeyHint == plaintext || !strings.Contains(row.KeyHint, "…") {
		t.Fatalf("unexpected stored secret fields: hash=%q hint=%q", row.KeyHash, row.KeyHint)
	}

	// The plaintext must not appear in any persisted column.
	var raw []map[string]any
	gdb.Raw("SELECT * FROM client_keys").Scan(&raw)
	for _, rec := range raw {
		for col, v := range rec {
			if s, ok := v.(string); ok && strings.Contains(s, plaintext) {
				t.Fatalf("plaintext key persisted in column %s", col)
			}
		}
	}

	caller, disabled, ok := Resolve(gdb, plaintext)
	if !ok || disabled || caller.Name != "pkm-app" || caller.ID != row.ID {
		t.Fatalf("resolve failed: ok=%v disabled=%v caller=%+v", ok, disabled, caller)
	}
	if got := strings.Join(caller.AllowedProviders, ","); got != "ollama,gemini" {
		t.Fatalf("providers not normalized: %q", got)
	}
	if !caller.Restricted() || !caller.AllowsProvider("OLLAMA") || caller.AllowsProvider("openrouter") {
		t.Fatalf("provider allowlist mismatch: %+v", caller)
	}
	if !caller.AllowsModel("notes-embed") || !caller.AllowsModel("gpt-4o") || caller.AllowsModel("gpt-4o-mini") {
		t.Fatalf("model allowlist mismatch: %+v", caller.AllowedModels)
	}
	if _, _, ok := Resolve(gdb, plaintext+"x"); ok {
		t.Fatal("wrong key must not resolve")
	}
	if !HasKeys(gdb) {
		t.Fatal("HasKeys must see the new key (cache invalidated on create)")
	}

	if _, _, err := Create(gdb, Spec{Name: "pkm-app"}); !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	for _, bad := range []string{"", "Legacy", "has space", "-lead"} {
		if _, _, err := Create(gdb, Spec{Name: bad}); err == nil {
			t.Fatalf("expected name %q rejected", bad)
		}
	}
}

func TestUpdateDisableClearAndDelete(t *testing.T) {
	gdb := openTestDB(t)
	row, plaintext, err := Create(gdb, Spec{Name: "lan-phone", AllowedProviders: []string{"gemini"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Prime the cache so the following updates must invalidate it.
	if _, _, ok := Resolve(gdb, plaintext); !ok {
		t.Fatal("expected key to resolve")
	}

	yes := true
	if _, err := Update(gdb, row.ID, Patch{Disabled: &yes}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, disabled, ok := Resolve(gdb, plaintext); !ok || !disabled {
		t.Fatalf("expected disabled key, ok=%v disabled=%v", ok, disabled)
	}

	no := false
	empty := []string{}
	updated, err := Update(gdb, row.ID, Patch{Disabled: &no, AllowedProviders: &empty})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if updated.Disabled || updated.AllowedProviders != "" {
		t.Fatalf("unexpected row after clear: %+v", updated)
	}
	caller, disabled, ok := Resolve(gdb, plaintext)
	if !ok || disabled || caller.Restricted() {
		t.Fatalf("expected enabled unrestricted key, got ok=%v disabled=%v caller=%+v", ok, disabled, caller)
	}

	byName, err := Get(gdb, "lan-phone")
	if err != nil || byName.ID != row.ID {
		t.Fatalf("get by name: %v %+v", err, byName)
	}

	Touch(gdb, row.ID)
	touched, _ := Get(gdb, "lan-phone")
	if touched.LastUsedAt == nil {
		t.Fatal("expected last_used_at to be recorded")
	}

	if err := Delete(gdb, row.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, ok := Resolve(gdb, plaintext); ok {
		t.Fatal("deleted key must not resolve")
	}
	if err := Delete(gdb, row.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := Update(gdb, row.ID, Patch{Disabled: &yes}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on update, got %v", err)
	}
}

func TestCallerPolicyEdgeCases(t *testing.T) {
	var none *Caller
	if none.Restricted() || !none.AllowsProvider("openrouter") || !none.AllowsModel("anything") {
		t.Fatal("nil caller (auth disabled) must be unrestricted")
	}
	if c := LegacyCaller(); c.Restricted() || !c.Legacy || c.Name != LegacyName {
		t.Fatalf("unexpected legacy caller %+v", c)
	}

	// A corrupt stored allowlist fails closed instead of granting everything.
	c := ToCaller(models.ClientKey{Name: "x", AllowedProviders: "not-json"})
	if !c.Restricted() || c.AllowsProvider("ollama") {
		t.Fatalf("corrupt allowlist must deny, got %+v", c)
	}

	// Missing table (pre-migration database) means no client keys rather than an error.
	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if HasKeys(gdb) {
		t.Fatal("expected no keys without table")
	}
}
