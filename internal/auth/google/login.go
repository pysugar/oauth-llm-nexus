package google

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"

	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

// stateToken is used to protect against CSRF attacks
var stateToken string

func init() {
	b := make([]byte, 16)
	rand.Read(b)
	stateToken = hex.EncodeToString(b)
}

// HandleLoginWithDB initiates the Google OAuth flow using a temporary callback server.
// Uses port 51121 (Antigravity IDE standard) if available, falls back to random high port.
func HandleLoginWithDB(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Start temporary callback server
		port, resultChan, cleanup, err := StartOAuthCallbackServer(db)
		if err != nil {
			log.Printf("[OAuth] Failed to start callback server: %v", err)
			http.Error(w, fmt.Sprintf("Failed to start OAuth flow: %v", err), http.StatusInternalServerError)
			return
		}

		// Use the actual port for redirect URI
		redirectURL := fmt.Sprintf("http://localhost:%d/oauth-callback", port)
		config := GetOAuthConfig(redirectURL)

		// Build auth URL
		url := config.AuthCodeURL(stateToken, oauth2.AccessTypeOffline, oauth2.ApprovalForce)

		log.Printf("[OAuth] Starting OAuth flow, callback on port %d", port)
		log.Printf("[OAuth] Auth URL: %s", url)

		// Redirect user to Google consent page
		http.Redirect(w, r, url, http.StatusTemporaryRedirect)

		// Wait for result in background (cleanup happens automatically)
		go func() {
			result := <-resultChan
			if result.Success {
				log.Printf("[OAuth] OAuth flow completed successfully")
			} else if result.Error != nil {
				log.Printf("[OAuth] OAuth flow failed: %v", result.Error)
			}
			cleanup()
		}()
	}
}

// HandleLogin is kept for backward compatibility (legacy, uses dynamic host)
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
