package google

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
)

// stateToken is used to protect against CSRF attacks
var stateToken string

func init() {
	b := make([]byte, 16)
	rand.Read(b)
	stateToken = hex.EncodeToString(b)
}

// isPrivateIP checks if the host is a private/local IP address
func isPrivateIP(host string) bool {
	// Remove port if present
	hostOnly := host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		hostOnly = host[:idx]
	}

	// Check for localhost
	if hostOnly == "localhost" || hostOnly == "127.0.0.1" {
		return false // localhost doesn't require device_id
	}

	ip := net.ParseIP(hostOnly)
	if ip == nil {
		return false
	}

	// Check private IP ranges: 10.x.x.x, 172.16-31.x.x, 192.168.x.x
	return ip.IsPrivate()
}

// HandleLogin initiates the Google OAuth flow by redirecting to Google's consent page.
func HandleLogin(w http.ResponseWriter, r *http.Request) {
	// Dynamically construct redirect URL from the request
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host // This includes the port if non-standard
	redirectURL := fmt.Sprintf("%s://%s/auth/google/callback", scheme, host)

	config := GetOAuthConfig(redirectURL)

	// Build OAuth options
	opts := []oauth2.AuthCodeOption{
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce,
	}

	// Google requires device_id and device_name for private IP addresses
	if isPrivateIP(host) {
		// Generate a unique device ID
		deviceID := make([]byte, 16)
		rand.Read(deviceID)
		opts = append(opts,
			oauth2.SetAuthURLParam("device_id", hex.EncodeToString(deviceID)),
			oauth2.SetAuthURLParam("device_name", "OAuth-LLM-Nexus"),
		)
	}

	url := config.AuthCodeURL(stateToken, opts...)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// GetStateToken returns the current CSRF state token for validation.
func GetStateToken() string {
	return stateToken
}
