package google

import (
	"crypto/rand"
	"encoding/hex"
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
	config := GetOAuthConfig("http://localhost:8080/auth/google/callback")
	url := config.AuthCodeURL(stateToken, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// GetStateToken returns the current CSRF state token for validation.
func GetStateToken() string {
	return stateToken
}
