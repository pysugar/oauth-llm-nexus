package google

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
)

// stateToken is used to protect against CSRF attacks
var stateToken string

func init() {
	b := make([]byte, 16)
	rand.Read(b)
	stateToken = hex.EncodeToString(b)
}

// HandleLogin initiates the Google OAuth flow by redirecting to Google's consent page.
func HandleLogin(w http.ResponseWriter, r *http.Request) {
	// IMPORTANT: OAuth callback MUST use localhost, not private IP addresses.
	// Google's Antigravity OAuth client only allows localhost callbacks.
	// Users must complete OAuth login on the machine running nexus.
	//
	// For headless/remote servers:
	// 1. Run nexus on a local machine first
	// 2. Complete OAuth login to generate nexus.db
	// 3. Copy nexus.db to the remote server
	//
	// Dynamically construct redirect URL from the request
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host // This includes the port if non-standard
	redirectURL := fmt.Sprintf("%s://%s/auth/google/callback", scheme, host)

	config := GetOAuthConfig(redirectURL)
	url := config.AuthCodeURL(stateToken, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// GetStateToken returns the current CSRF state token for validation.
func GetStateToken() string {
	return stateToken
}
