package google

import (
	"golang.org/x/oauth2"
	googleOAuth "golang.org/x/oauth2/google"
)

// OAuth credentials from Antigravity (for learning/research purposes)
const (
	ClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	ClientSecret = "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"
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
	return &oauth2.Config{
		ClientID:     ClientID,
		ClientSecret: ClientSecret,
		RedirectURL:  redirectURL,
		Scopes:       Scopes,
		Endpoint:     googleOAuth.Endpoint,
	}
}
