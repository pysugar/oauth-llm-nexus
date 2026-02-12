package token

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

func newTestTokenDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.AutoMigrate(&models.Account{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

func TestReloadAllTokens_RebuildsCache(t *testing.T) {
	db := newTestTokenDB(t)
	acc := models.Account{
		ID:          "acc-1",
		Email:       "test@example.com",
		Provider:    "google",
		AccessToken: "token-1",
		ExpiresAt:   time.Now().Add(time.Hour),
		IsActive:    true,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatalf("create account: %v", err)
	}

	mgr := NewManager(db)
	if len(mgr.cache) != 1 {
		t.Fatalf("expected 1 cached account, got %d", len(mgr.cache))
	}

	if err := db.Model(&models.Account{}).Where("id = ?", acc.ID).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate account: %v", err)
	}

	mgr.ReloadAllTokens()
	if len(mgr.cache) != 0 {
		t.Fatalf("expected cache to be rebuilt and empty, got %d", len(mgr.cache))
	}
}

func TestIsPermanentRefreshError(t *testing.T) {
	tests := []struct {
		name      string
		errText   string
		permanent bool
	}{
		{name: "invalid grant", errText: "oauth2: cannot fetch token: 400 Bad Request {\"error\":\"invalid_grant\"}", permanent: true},
		{name: "revoked", errText: "token has been expired or revoked", permanent: true},
		{name: "timeout", errText: "context deadline exceeded", permanent: false},
		{name: "temporary", errText: "temporarily_unavailable", permanent: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPermanentRefreshError(assertErr(tt.errText))
			if got != tt.permanent {
				t.Fatalf("expected %v, got %v", tt.permanent, got)
			}
		})
	}
}

func TestRefreshAccountToken_ReactivatesInactiveAccount(t *testing.T) {
	db := newTestTokenDB(t)
	acc := models.Account{
		ID:           "acc-refresh",
		Email:        "refresh@example.com",
		Provider:     "google",
		AccessToken:  "old-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour),
		IsActive:     false,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatalf("create account: %v", err)
	}

	oldRefreshOAuthToken := refreshOAuthToken
	defer func() { refreshOAuthToken = oldRefreshOAuthToken }()

	refreshOAuthToken = func(refreshToken string) (*oauth2.Token, error) {
		if refreshToken != "refresh-token" {
			t.Fatalf("unexpected refresh token: %s", refreshToken)
		}
		return &oauth2.Token{
			AccessToken: "new-token",
			Expiry:      time.Now().Add(time.Hour),
		}, nil
	}

	mgr := NewManager(db)
	if err := mgr.RefreshAccountToken(acc.ID); err != nil {
		t.Fatalf("refresh inactive account should succeed, got err=%v", err)
	}

	var updated models.Account
	if err := db.First(&updated, "id = ?", acc.ID).Error; err != nil {
		t.Fatalf("reload updated account: %v", err)
	}
	if !updated.IsActive {
		t.Fatal("expected inactive account to be reactivated after manual refresh")
	}
	if updated.AccessToken != "new-token" {
		t.Fatalf("expected access token to be updated, got %q", updated.AccessToken)
	}

	cached, exists := mgr.cache[acc.ID]
	if !exists {
		t.Fatal("expected refreshed account to be inserted into cache")
	}
	if cached.AccessToken != "new-token" {
		t.Fatalf("expected cached token to be updated, got %q", cached.AccessToken)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
