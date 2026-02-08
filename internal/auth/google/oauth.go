package google

import (
	"os"
	"strings"

	"golang.org/x/oauth2"
	googleOAuth "golang.org/x/oauth2/google"
)

// OAuth credentials from Antigravity (for learning/research purposes)
// Default values are used if environment variables are not set.
const (
	DefaultClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	DefaultClientSecret = "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"
)

// Scopes required for accessing Google's internal Gemini API
var Scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// GetOAuthConfig returns the OAuth2 config for Google authentication.
func GetOAuthConfig(redirectURL string) *oauth2.Config {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	if clientID == "" {
		clientID = DefaultClientID
	}

	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if clientSecret == "" {
		clientSecret = DefaultClientSecret
	}

	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       Scopes,
		Endpoint:     googleOAuth.Endpoint,
	}
}

// IsUsingDefaultOAuthCredentials returns true when either credential is using built-in defaults.
func IsUsingDefaultOAuthCredentials() bool {
	clientID := strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_SECRET"))
	return clientID == "" || clientSecret == ""
}
