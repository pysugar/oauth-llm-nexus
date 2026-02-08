package token

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/pysugar/oauth-llm-nexus/internal/auth/google"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

// Manager handles token lifecycle including auto-refresh
type Manager struct {
	db    *gorm.DB
	cache map[string]*CachedToken
	mu    sync.RWMutex
}

// CachedToken holds an in-memory token with its metadata
type CachedToken struct {
	AccessToken string
	ExpiresAt   time.Time
	ProjectID   string
	Email       string
}

// NewManager creates a new token manager
func NewManager(db *gorm.DB) *Manager {
	m := &Manager{
		db:    db,
		cache: make(map[string]*CachedToken),
	}
	m.loadAllTokens()
	return m
}

// loadAllTokens loads all active tokens into memory
func (m *Manager) loadAllTokens() {
	var accounts []models.Account
	m.db.Where("is_active = ?", true).Find(&accounts)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Full rebuild keeps cache consistent with DB active set.
	m.cache = make(map[string]*CachedToken, len(accounts))
	for _, acc := range accounts {
		m.cache[acc.ID] = &CachedToken{
			AccessToken: acc.AccessToken,
			ExpiresAt:   acc.ExpiresAt,
			ProjectID:   extractProjectID(acc.Metadata),
			Email:       acc.Email,
		}
	}
	log.Printf("📦 Loaded %d accounts into cache", len(accounts))
}

// ReloadAllTokens reloads the token cache from the database (public API)
func (m *Manager) ReloadAllTokens() {
	m.loadAllTokens()
}

// GetToken retrieves a valid access token, prioritizing the Primary account
func (m *Manager) GetToken(provider string) (*CachedToken, error) {
	// First, try to find the Primary account from DB
	var primaryAccount models.Account
	if err := m.db.Where("is_primary = ? AND is_active = ?", true, true).First(&primaryAccount).Error; err == nil {
		// Found primary, check cache or load
		token, err := m.GetTokenByAccountID(primaryAccount.ID)
		if err == nil && token.ExpiresAt.After(time.Now().Add(5*time.Minute)) {
			log.Printf("🎫 Using PRIMARY token for: %s", token.Email)
			return token, nil
		}
	}

	// Fall back to first valid token in cache
	m.mu.RLock()
	defer m.mu.RUnlock()

	for id, token := range m.cache {
		if token.ExpiresAt.After(time.Now().Add(5 * time.Minute)) {
			log.Printf("🎫 Using token for: %s (fallback)", token.Email)
			return token, nil
		}
		// Token expired or expiring soon, trigger refresh
		go m.refreshToken(id)
	}

	return nil, fmt.Errorf("no valid tokens available")
}

// GetTokenByAccountID returns a valid access token for a specific account
func (m *Manager) GetTokenByAccountID(accountID string) (*CachedToken, error) {
	m.mu.RLock()
	token, exists := m.cache[accountID]
	m.mu.RUnlock()

	if !exists {
		// Not in cache, try loading from database
		var account models.Account
		if err := m.db.Where("id = ? AND is_active = ?", accountID, true).First(&account).Error; err != nil {
			return nil, fmt.Errorf("account not found or inactive: %s", accountID)
		}

		// Add to cache
		token = &CachedToken{
			AccessToken: account.AccessToken,
			ExpiresAt:   account.ExpiresAt,
			ProjectID:   extractProjectID(account.Metadata),
			Email:       account.Email,
		}
		m.mu.Lock()
		m.cache[accountID] = token
		m.mu.Unlock()
		log.Printf("📦 Loaded account %s into cache on-demand", account.Email)
	}

	if token.ExpiresAt.Before(time.Now().Add(time.Minute)) {
		// Token expiring soon, trigger refresh synchronously
		log.Printf("⚠️ Token for %s is expired/expiring, refreshing...", token.Email)
		m.refreshToken(accountID)

		// Re-read from cache
		m.mu.RLock()
		token = m.cache[accountID]
		m.mu.RUnlock()

		if token == nil || token.ExpiresAt.Before(time.Now().Add(time.Minute)) {
			return nil, fmt.Errorf("token refresh failed")
		}
	}

	return token, nil
}

// RefreshAccountToken forces a refresh for a specific account
func (m *Manager) RefreshAccountToken(accountID string) error {
	m.refreshToken(accountID)

	// Verify refresh success
	m.mu.RLock()
	token, exists := m.cache[accountID]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("account not found in cache")
	}
	if token.ExpiresAt.Before(time.Now().Add(time.Minute)) {
		return fmt.Errorf("refresh failed, token still expired")
	}
	return nil
}

func maskToken(t string) string {
	if len(t) < 20 {
		return t
	}
	return "..." + t[len(t)-12:]
}

// GetPrimaryOrDefaultToken returns token for Primary account, or first active if no primary set
func (m *Manager) GetPrimaryOrDefaultToken() (*CachedToken, error) {
	// 1. Try to find Primary account
	var primaryAccount models.Account
	if err := m.db.Where("is_primary = ? AND is_active = ?", true, true).First(&primaryAccount).Error; err == nil {
		token, err := m.GetTokenByAccountID(primaryAccount.ID)
		if err == nil {
			log.Printf("🎫 Using PRIMARY token for: %s (ID: %s, Token: %s)", token.Email, primaryAccount.ID, maskToken(token.AccessToken))
			return token, nil
		}
	}

	// 2. Fallback to first active account (ordered by last used descending)
	var fallbackAccount models.Account
	if err := m.db.Where("is_active = ?", true).Order("last_used_at DESC").First(&fallbackAccount).Error; err == nil {
		token, err := m.GetTokenByAccountID(fallbackAccount.ID)
		if err == nil {
			log.Printf("⚠️ Using FALLBACK token for: %s (ID: %s, Token: %s)", token.Email, fallbackAccount.ID, maskToken(token.AccessToken))
			return token, nil
		}
	}

	return nil, fmt.Errorf("no active accounts available")
}

// GetTokenByIdentifier finds account by email or ID and returns its token
func (m *Manager) GetTokenByIdentifier(identifier string) (*CachedToken, error) {
	var account models.Account
	// Try by email first
	if err := m.db.Where("email = ? AND is_active = ?", identifier, true).First(&account).Error; err == nil {
		return m.GetTokenByAccountID(account.ID)
	}
	// Try by ID
	if err := m.db.Where("id = ? AND is_active = ?", identifier, true).First(&account).Error; err == nil {
		return m.GetTokenByAccountID(account.ID)
	}
	return nil, fmt.Errorf("account not found: %s", identifier)
}

// StartRefreshLoop starts background token refresh
func (m *Manager) StartRefreshLoop() {
	ticker := time.NewTicker(15 * time.Minute) // Reduced frequency to minimize Google OAuth load
	go func() {
		for range ticker.C {
			m.refreshExpiredTokens()
		}
	}()
	log.Println("🔄 Token refresh loop started (interval: 15min)")
}

// refreshExpiredTokens refreshes all tokens expiring within 20 minutes
func (m *Manager) refreshExpiredTokens() {
	var accounts []models.Account
	threshold := time.Now().Add(20 * time.Minute)
	m.db.Where("is_active = ? AND expires_at < ?", true, threshold).Find(&accounts)

	for _, acc := range accounts {
		m.refreshToken(acc.ID)
	}
}

// RefreshAllTokens triggers refresh for all active tokens (public API)
func (m *Manager) RefreshAllTokens() {
	var accounts []models.Account
	m.db.Where("is_active = ?", true).Find(&accounts)

	for _, acc := range accounts {
		go m.refreshToken(acc.ID)
	}
	log.Printf("🔄 Triggered refresh for %d accounts", len(accounts))
}

// refreshToken refreshes a single token
func (m *Manager) refreshToken(accountID string) {
	var account models.Account
	if err := m.db.First(&account, "id = ?", accountID).Error; err != nil {
		log.Printf("⚠️ Failed to find account %s: %v", accountID, err)
		return
	}

	// Use OAuth2 token source for refresh
	token := &oauth2.Token{
		RefreshToken: account.RefreshToken,
	}

	config := google.GetOAuthConfig("")
	tokenSource := config.TokenSource(context.Background(), token)

	newToken, err := tokenSource.Token()
	if err != nil {
		log.Printf("❌ Refresh token failed for %s: %v", account.Email, err)

		if isPermanentRefreshError(err) {
			// Permanent auth failures should deactivate account and require re-login.
			account.IsActive = false
			m.db.Save(&account)

			m.mu.Lock()
			delete(m.cache, accountID)
			m.mu.Unlock()

			log.Printf("🔒 Account %s marked as inactive. Please re-login.", account.Email)
			return
		}

		// Transient failure: keep account active and retry later.
		log.Printf("⏳ Transient refresh failure for %s, account remains active", account.Email)
		return
	}

	// Update database
	account.AccessToken = newToken.AccessToken
	account.ExpiresAt = newToken.Expiry
	account.LastUsedAt = time.Now()
	account.IsActive = true
	// Persist rotated refresh token if provided (RFC 6749 compliance)
	if newToken.RefreshToken != "" && newToken.RefreshToken != account.RefreshToken {
		log.Printf("🔄 Rotating refresh token for: %s", account.Email)
		account.RefreshToken = newToken.RefreshToken
	}
	if err := m.db.Save(&account).Error; err != nil {
		log.Printf("⚠️ Failed to save refreshed token: %v", err)
		return
	}

	// Update cache
	m.mu.Lock()
	m.cache[accountID] = &CachedToken{
		AccessToken: newToken.AccessToken,
		ExpiresAt:   newToken.Expiry,
		ProjectID:   extractProjectID(account.Metadata),
		Email:       account.Email,
	}
	m.mu.Unlock()

	log.Printf("✅ Refreshed token for: %s (expires: %s)", account.Email, newToken.Expiry.Format(time.RFC3339))
}

// extractProjectID extracts project_id from metadata JSON
func extractProjectID(metadata string) string {
	if metadata == "" {
		return "bamboo-precept-lgxtn"
	}

	var data map[string]string
	if err := json.Unmarshal([]byte(metadata), &data); err != nil {
		return "bamboo-precept-lgxtn"
	}

	if pid, ok := data["project_id"]; ok && pid != "" {
		return pid
	}
	return "bamboo-precept-lgxtn"
}

func isPermanentRefreshError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	permanentMarkers := []string{
		"invalid_grant",
		"invalid_client",
		"unauthorized_client",
		"token has been expired or revoked",
		"revoked",
	}
	for _, marker := range permanentMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
