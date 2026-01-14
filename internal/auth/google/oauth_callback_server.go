package google

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
)

const (
	// AntigravityCallbackPort is the preferred callback port (same as Antigravity IDE)
	AntigravityCallbackPort = 51121
	// CallbackTimeout is how long to wait for the OAuth callback
	CallbackTimeout = 5 * time.Minute
)

// OAuthCallbackResult contains the result of the OAuth flow
type OAuthCallbackResult struct {
	Success bool
	Error   error
}

// StartOAuthCallbackServer starts a temporary HTTP server to receive the OAuth callback.
// It tries to use port 51121 first (Antigravity IDE standard), falls back to a random high port.
// Returns the actual port used, a channel to receive the result, and a cleanup function.
func StartOAuthCallbackServer(db *gorm.DB) (actualPort int, resultChan <-chan OAuthCallbackResult, cleanup func(), err error) {
	// Try preferred port 51121 first
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", AntigravityCallbackPort))
	if err != nil {
		// Port 51121 is in use, fall back to random high port (system assigns from 49152-65535)
		listener, err = net.Listen("tcp", ":0")
		if err != nil {
			return 0, nil, nil, fmt.Errorf("failed to start callback server: %w", err)
		}
		log.Printf("[OAuth] Port %d in use, using random port", AntigravityCallbackPort)
	}

	actualPort = listener.Addr().(*net.TCPAddr).Port
	log.Printf("[OAuth] Callback server listening on port %d", actualPort)

	// Create result channel
	resultChannel := make(chan OAuthCallbackResult, 1)

	// Create server
	mux := http.NewServeMux()
	srv := &http.Server{Handler: mux}

	// Track if callback was received
	callbackReceived := false

	// Handle OAuth callback
	mux.HandleFunc("/oauth-callback", func(w http.ResponseWriter, r *http.Request) {
		if callbackReceived {
			http.Error(w, "Callback already processed", http.StatusBadRequest)
			return
		}
		callbackReceived = true

		// Verify state token
		state := r.URL.Query().Get("state")
		if state != GetStateToken() {
			resultChannel <- OAuthCallbackResult{Success: false, Error: fmt.Errorf("invalid state token")}
			http.Error(w, "Invalid state token", http.StatusBadRequest)
			return
		}

		// Exchange authorization code for tokens
		code := r.URL.Query().Get("code")
		redirectURL := fmt.Sprintf("http://localhost:%d/oauth-callback", actualPort)
		config := GetOAuthConfig(redirectURL)

		token, err := config.Exchange(context.Background(), code)
		if err != nil {
			resultChannel <- OAuthCallbackResult{Success: false, Error: fmt.Errorf("token exchange failed: %w", err)}
			http.Error(w, fmt.Sprintf("Token exchange failed: %v", err), http.StatusInternalServerError)
			return
		}

		// Fetch user info from Google
		client := config.Client(context.Background(), token)
		resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
		if err != nil {
			resultChannel <- OAuthCallbackResult{Success: false, Error: fmt.Errorf("failed to get user info: %w", err)}
			http.Error(w, fmt.Sprintf("Failed to get user info: %v", err), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		var userInfo struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
			resultChannel <- OAuthCallbackResult{Success: false, Error: fmt.Errorf("failed to decode user info: %w", err)}
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
		var existingAccount models.Account
		var accountID string
		var isPrimary bool

		if err := db.Where("email = ?", userInfo.Email).First(&existingAccount).Error; err == nil {
			// Account exists, preserve UUID, update tokens
			accountID = existingAccount.ID
			isPrimary = existingAccount.IsPrimary

			existingAccount.AccessToken = token.AccessToken
			existingAccount.RefreshToken = token.RefreshToken
			existingAccount.ExpiresAt = token.Expiry
			existingAccount.Metadata = string(metadata)

			if err := db.Save(&existingAccount).Error; err != nil {
				resultChannel <- OAuthCallbackResult{Success: false, Error: fmt.Errorf("failed to update account: %w", err)}
				http.Error(w, fmt.Sprintf("Failed to update account: %v", err), http.StatusInternalServerError)
				return
			}
			log.Printf("[OAuth] Updated existing account: %s (ID: %s)", userInfo.Email, accountID)
		} else {
			// New account
			accountID = uuid.New().String()

			// Check if this is the first account (make it primary)
			var count int64
			db.Model(&models.Account{}).Count(&count)
			isPrimary = count == 0

			account := models.Account{
				ID:           accountID,
				Email:        userInfo.Email,
				AccessToken:  token.AccessToken,
				RefreshToken: token.RefreshToken,
				ExpiresAt:    token.Expiry,
				IsPrimary:    isPrimary,
				Metadata:     string(metadata),
			}

			if err := db.Create(&account).Error; err != nil {
				// Handle duplicate email (race condition)
				if strings.Contains(err.Error(), "UNIQUE constraint failed") {
					resultChannel <- OAuthCallbackResult{Success: false, Error: fmt.Errorf("account already exists")}
					http.Error(w, "Account already exists", http.StatusConflict)
					return
				}
				resultChannel <- OAuthCallbackResult{Success: false, Error: fmt.Errorf("failed to save account: %w", err)}
				http.Error(w, fmt.Sprintf("Failed to save account: %v", err), http.StatusInternalServerError)
				return
			}
			log.Printf("[OAuth] Created new account: %s (ID: %s, Primary: %v)", userInfo.Email, accountID, isPrimary)
		}

		// Return success HTML
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Return success HTML with countdown
		// Determine dashboard port from environment
		dashboardPort := os.Getenv("PORT")
		if dashboardPort == "" {
			// Match main.go logic: release mode uses 8086, dev uses 8080
			if os.Getenv("NEXUS_MODE") == "release" {
				dashboardPort = "8086"
			} else {
				dashboardPort = "8080"
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
	<meta charset="UTF-8">
	<title>Login Successful</title>
	<style>
		body { font-family: -apple-system, BlinkMacSystemFont, sans-serif; max-width: 600px; margin: 50px auto; padding: 20px; background: #1a1a2e; color: #eee; text-align: center; }
		.success { color: #4ade80; font-size: 24px; margin-bottom: 10px; }
		code { background: #374151; padding: 2px 6px; border-radius: 4px; color: #fbbf24; }
		.redirect { color: #9ca3af; margin-top: 30px; }
		.btn { display: inline-block; margin-top: 20px; padding: 10px 20px; background: #3b82f6; color: white; text-decoration: none; border-radius: 6px; }
		.btn:hover { background: #2563eb; }
	</style>
</head>
<body>
	<div class="success">✅ Login Successful</div>
	<p>Account <strong>%s</strong> has been added.</p>
	<p>Project ID: <code>%s</code></p>
	<p>Subscription: <strong>%s</strong></p>
	
	<div class="redirect">
		<p>Redirecting to dashboard in <span id="countdown">5</span> seconds...</p>
		<a href="http://localhost:%s/" class="btn">Go to Dashboard</a>
	</div>

	<script>
		let sec = 5;
		const el = document.getElementById('countdown');
		const interval = setInterval(() => {
			sec--;
			el.textContent = sec;
			if (sec <= 0) {
				clearInterval(interval);
				window.location.href = "http://localhost:%s/";
			}
		}, 1000);
	</script>
</body>
</html>`, userInfo.Email, projectID, subscriptionTier, dashboardPort, dashboardPort)

		resultChannel <- OAuthCallbackResult{Success: true, Error: nil}
	})

	// Start server in background
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[OAuth] Callback server error: %v", err)
		}
	}()

	// Cleanup function
	var once sync.Once
	cleanup = func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				log.Printf("[OAuth] Error shutting down callback server: %v", err)
			}
			close(resultChannel)
			log.Printf("[OAuth] Callback server stopped")
		})
	}

	// Auto-cleanup after timeout
	go func() {
		time.Sleep(CallbackTimeout)
		if !callbackReceived {
			log.Printf("[OAuth] Callback timeout after %v", CallbackTimeout)
			// Non-blocking send in case channel is already closed/full (though cleanup handles close)
			select {
			case resultChannel <- OAuthCallbackResult{Success: false, Error: fmt.Errorf("OAuth callback timeout")}:
			default:
			}
		}
		cleanup()
	}()

	return actualPort, resultChannel, cleanup, nil
}
