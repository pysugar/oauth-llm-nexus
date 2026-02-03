package codex

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// TokenURL is the OpenAI OAuth token refresh endpoint
	TokenURL = "https://auth.openai.com/oauth/token"
	// ClientID is the Codex CLI client ID
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// RefreshMargin is how early to refresh before expiration
	RefreshMargin = 5 * time.Minute
)

// AuthJSON represents the structure of ~/.codex/auth.json
type AuthJSON struct {
	OpenAIAPIKey *string    `json:"OPENAI_API_KEY"`
	Tokens       *TokenData `json:"tokens"`
	LastRefresh  string     `json:"last_refresh"`
}

// TokenData contains the OAuth tokens
type TokenData struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

// TokenManager manages Codex OAuth tokens
type TokenManager struct {
	authPath   string
	authData   *AuthJSON
	expireTime time.Time
	email      string
	planType   string
	mu         sync.RWMutex
	httpClient *http.Client
}

// NewTokenManager creates a new TokenManager
func NewTokenManager(authPath string) *TokenManager {
	if authPath == "" {
		home, _ := os.UserHomeDir()
		authPath = filepath.Join(home, ".codex", "auth.json")
	}
	return &TokenManager{
		authPath:   authPath,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Load reads auth.json and parses token information
func (m *TokenManager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.authPath)
	if err != nil {
		return fmt.Errorf("failed to read auth.json: %w", err)
	}

	var auth AuthJSON
	if err := json.Unmarshal(data, &auth); err != nil {
		return fmt.Errorf("failed to parse auth.json: %w", err)
	}

	if auth.Tokens == nil || auth.Tokens.AccessToken == "" {
		return fmt.Errorf("no valid tokens in auth.json")
	}

	m.authData = &auth

	// Parse id_token for user info (email, plan_type)
	if idClaims, err := ParseJWT(auth.Tokens.IDToken); err == nil {
		m.email = idClaims.Email
		m.planType = idClaims.AuthInfo.ChatgptPlanType
	}
	// Parse access_token for expiration time
	if accessClaims, err := ParseJWT(auth.Tokens.AccessToken); err == nil {
		m.expireTime = time.Unix(accessClaims.Exp, 0)
	}

	log.Printf("✅ Codex auth loaded: email=%s, plan=%s, expires=%s",
		m.email, m.planType, m.expireTime.Format(time.RFC3339))
	return nil
}

// GetAccessToken returns a valid access token, refreshing if necessary
func (m *TokenManager) GetAccessToken() (string, error) {
	m.mu.RLock()
	if m.authData != nil && time.Now().Before(m.expireTime.Add(-RefreshMargin)) {
		defer m.mu.RUnlock()
		return m.authData.Tokens.AccessToken, nil
	}
	m.mu.RUnlock()

	return m.refreshToken()
}

// GetAccountInfo returns account information for quota display
func (m *TokenManager) GetAccountInfo() (email, planType, accountID string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.authData == nil || m.authData.Tokens == nil {
		return "", "", ""
	}
	return m.email, m.planType, m.authData.Tokens.AccountID
}

// refreshToken refreshes the access token using the refresh token
func (m *TokenManager) refreshToken() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Try reloading from file first (Codex CLI may have refreshed it)
	if err := m.loadLocked(); err == nil {
		if time.Now().Before(m.expireTime.Add(-RefreshMargin)) {
			return m.authData.Tokens.AccessToken, nil
		}
	}

	if m.authData == nil || m.authData.Tokens == nil {
		return "", fmt.Errorf("no auth data loaded")
	}

	log.Printf("🔄 Refreshing Codex access token...")

	// Call refresh API
	data := url.Values{
		"client_id":     {ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {m.authData.Tokens.RefreshToken},
		"scope":         {"openid profile email"},
	}

	resp, err := m.httpClient.PostForm(TokenURL, data)
	if err != nil {
		return "", fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse refresh response: %w", err)
	}

	// Update in-memory state
	m.authData.Tokens.AccessToken = tokenResp.AccessToken
	m.authData.Tokens.IDToken = tokenResp.IDToken
	if tokenResp.RefreshToken != "" {
		m.authData.Tokens.RefreshToken = tokenResp.RefreshToken
	}
	m.authData.LastRefresh = time.Now().UTC().Format(time.RFC3339Nano)
	m.expireTime = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	// Update user info from new id_token
	if idClaims, err := ParseJWT(tokenResp.IDToken); err == nil {
		m.email = idClaims.Email
		m.planType = idClaims.AuthInfo.ChatgptPlanType
	}

	// Save back to file
	if err := m.saveToFile(); err != nil {
		log.Printf("⚠️ Failed to save refreshed auth.json: %v", err)
	}

	log.Printf("✅ Codex token refreshed, expires at %s", m.expireTime.Format(time.RFC3339))
	return tokenResp.AccessToken, nil
}

// loadLocked loads auth.json (caller must hold lock)
func (m *TokenManager) loadLocked() error {
	data, err := os.ReadFile(m.authPath)
	if err != nil {
		return err
	}
	var auth AuthJSON
	if err := json.Unmarshal(data, &auth); err != nil {
		return err
	}
	if auth.Tokens == nil {
		return fmt.Errorf("no tokens")
	}
	m.authData = &auth
	if idClaims, _ := ParseJWT(auth.Tokens.IDToken); idClaims != nil {
		m.email = idClaims.Email
		m.planType = idClaims.AuthInfo.ChatgptPlanType
	}
	if accessClaims, _ := ParseJWT(auth.Tokens.AccessToken); accessClaims != nil {
		m.expireTime = time.Unix(accessClaims.Exp, 0)
	}
	return nil
}

// saveToFile writes the current auth data back to auth.json
func (m *TokenManager) saveToFile() error {
	data, err := json.MarshalIndent(m.authData, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.authPath, data, 0600)
}
