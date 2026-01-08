package google

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
)

// HandleCallback processes the OAuth callback from Google.
func HandleCallback(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Verify state token
		state := r.URL.Query().Get("state")
		if state != GetStateToken() {
			http.Error(w, "Invalid state token", http.StatusBadRequest)
			return
		}

		// Exchange authorization code for tokens
		code := r.URL.Query().Get("code")

		// Dynamically construct redirect URL from the request
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		host := r.Host
		redirectURL := fmt.Sprintf("%s://%s/auth/google/callback", scheme, host)

		config := GetOAuthConfig(redirectURL)

		token, err := config.Exchange(context.Background(), code)
		if err != nil {
			http.Error(w, fmt.Sprintf("Token exchange failed: %v", err), http.StatusInternalServerError)
			return
		}

		// Fetch user info from Google
		client := config.Client(context.Background(), token)
		resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get user info: %v", err), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		var userInfo struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
			http.Error(w, fmt.Sprintf("Failed to decode user info: %v", err), http.StatusInternalServerError)
			return
		}

		// Fetch project ID and subscription tier from loadCodeAssist
		projectID, subscriptionTier := fetchProjectInfo(client)

		// Store metadata as JSON
		metadata, _ := json.Marshal(map[string]string{
			"project_id":        projectID,
			"name":              userInfo.Name,
			"subscription_tier": subscriptionTier,
		})

		// Save or update account in database
		// Check for existing account to preserve UUID
		var existingAccount models.Account
		var accountID string
		var isPrimary bool

		err = db.Where("email = ? AND provider = ?", userInfo.Email, "google").First(&existingAccount).Error
		if err == nil {
			accountID = existingAccount.ID
			isPrimary = existingAccount.IsPrimary
		} else {
			accountID = uuid.New().String()
			// Check if any primary account exists - if not, make this one primary
			var primaryCount int64
			db.Model(&models.Account{}).Where("is_primary = ?", true).Count(&primaryCount)
			isPrimary = (primaryCount == 0)
		}

		account := models.Account{
			ID:           accountID,
			Email:        userInfo.Email,
			Provider:     "google",
			AccessToken:  token.AccessToken,
			RefreshToken: token.RefreshToken,
			ExpiresAt:    token.Expiry,
			LastUsedAt:   time.Now(),
			IsActive:     true,
			IsPrimary:    isPrimary,
			Scopes:       fmt.Sprintf("%v", Scopes),
			Metadata:     string(metadata),
		}

		if err := db.Save(&account).Error; err != nil {
			http.Error(w, fmt.Sprintf("Failed to save account: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
	<meta charset="UTF-8">
	<meta http-equiv="refresh" content="3;url=/">
	<title>Login Successful</title>
	<style>
		body { font-family: -apple-system, BlinkMacSystemFont, sans-serif; max-width: 600px; margin: 50px auto; padding: 20px; background: #1a1a2e; color: #eee; }
		.success { color: #4ade80; }
		code { background: #374151; padding: 2px 6px; border-radius: 4px; color: #fbbf24; }
		.redirect { color: #9ca3af; margin-top: 20px; }
	</style>
</head>
<body>
	<h1 class="success">✅ Login Successful!</h1>
	<p><strong>Email:</strong> %s</p>
	<p><strong>Provider:</strong> Google</p>
	<p><strong>Project ID:</strong> <code>%s</code></p>
	<p class="redirect">Redirecting to dashboard in <span id="countdown">3</span> seconds...</p>
	<script>
		let sec = 3;
		setInterval(() => { if(sec > 0) document.getElementById('countdown').textContent = --sec; }, 1000);
		setTimeout(() => window.location.href = '/', 3000);
	</script>
</body>
</html>`, userInfo.Email, projectID)
	}
}

// fetchProjectInfo calls the loadCodeAssist endpoint to get project ID and subscription tier.
func fetchProjectInfo(client *http.Client) (projectID string, subscriptionTier string) {
	// Build request with proper headers like Antigravity-Manager's project_resolver.rs
	reqBody := strings.NewReader(`{"metadata": {"ideType": "ANTIGRAVITY"}}`)
	req, err := http.NewRequest("POST", "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", reqBody)
	if err != nil {
		log.Printf("⚠️  Failed to create loadCodeAssist request: %v", err)
		return "bamboo-precept-lgxtn", "FREE"
	}

	// Set headers to match Antigravity-Manager exactly
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", "cloudcode-pa.googleapis.com")
	req.Header.Set("User-Agent", "antigravity/1.11.9 windows/amd64")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("⚠️  loadCodeAssist API error: %v", err)
		return "bamboo-precept-lgxtn", "FREE"
	}
	defer resp.Body.Close()

	// Read response body for logging
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("⚠️  Failed to read loadCodeAssist response: %v", err)
		return "bamboo-precept-lgxtn", "FREE"
	}

	// Log the raw response
	log.Printf("📋 loadCodeAssist API response: %s", string(bodyBytes))

	var result struct {
		// Antigravity-Manager format
		CloudaicompanionProject string `json:"cloudaicompanionProject"`
		PaidTier                *struct {
			ID        string `json:"id"`
			QuotaTier string `json:"quotaTier"`
			Name      string `json:"name"`
		} `json:"paidTier"`
		CurrentTier *struct {
			ID        string `json:"id"`
			QuotaTier string `json:"quotaTier"`
			Name      string `json:"name"`
		} `json:"currentTier"`
		// Fallback format
		Config struct {
			ProjectID string `json:"projectId"`
		} `json:"codeAssistConfig"`
		ManageSubscriptionUri string `json:"manageSubscriptionUri"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		log.Printf("⚠️  Failed to parse loadCodeAssist response: %v", err)
		return "bamboo-precept-lgxtn", "FREE"
	}

	// Get project ID
	projectID = result.CloudaicompanionProject
	if projectID == "" {
		projectID = result.Config.ProjectID
	}
	if projectID == "" {
		projectID = "bamboo-precept-lgxtn"
	}

	// Tier detection: prefer paidTier > currentTier > manageSubscriptionUri > FREE
	if result.PaidTier != nil && result.PaidTier.ID != "" {
		subscriptionTier = result.PaidTier.ID
		log.Printf("📊 Tier from paidTier: %s", subscriptionTier)
	} else if result.CurrentTier != nil && result.CurrentTier.ID != "" {
		subscriptionTier = result.CurrentTier.ID
		log.Printf("📊 Tier from currentTier: %s", subscriptionTier)
	} else if result.ManageSubscriptionUri != "" {
		subscriptionTier = "PRO"
		log.Printf("📊 Tier from manageSubscriptionUri: PRO")
	} else {
		subscriptionTier = "FREE"
		log.Printf("📊 Tier defaulted to: FREE")
	}

	log.Printf("📊 Final - ProjectID: %s, Tier: %s", projectID, subscriptionTier)
	return projectID, subscriptionTier
}
