package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"github.com/pysugar/oauth-llm-nexus/internal/discovery"
	"gorm.io/gorm"
)

// DiscoveryScanHandler scans for credentials and returns masked results
func DiscoveryScanHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result := discovery.ScanAll()

		// Mask tokens for API response
		maskedCreds := make([]discovery.Credential, len(result.Credentials))
		for i, cred := range result.Credentials {
			maskedCreds[i] = discovery.MaskCredential(cred)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"credentials": maskedCreds,
			"errors":      result.Errors,
			"count":       len(result.Credentials),
		})
	}
}

// ConfigCheckHandler checks all IDE configs and returns their current state
func ConfigCheckHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := discovery.CheckAllConfigs()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(report)
	}
}

// DiscoveryImportRequest represents a request to import a discovered credential
type DiscoveryImportRequest struct {
	Source string `json:"source"`
	Index  int    `json:"index"` // Index in the scan result
	Email  string `json:"email"` // User-provided or confirmed email
}

// DiscoveryImportHandler imports a discovered credential to the database
func DiscoveryImportHandler(database *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req DiscoveryImportRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error": "Invalid request body"}`, http.StatusBadRequest)
			return
		}

		// Re-scan to get the actual tokens (not masked)
		result := discovery.ScanAll()
		
		// Find the credential by source and index
		var cred *discovery.Credential
		idx := 0
		for _, c := range result.Credentials {
			if c.Source == req.Source {
				if idx == req.Index {
					cred = &c
					break
				}
				idx++
			}
		}

		if cred == nil {
			http.Error(w, `{"error": "Credential not found"}`, http.StatusNotFound)
			return
		}

		// Use provided email or discovered email
		email := req.Email
		if email == "" {
			email = cred.Email
		}
		if email == "" {
			http.Error(w, `{"error": "Email is required"}`, http.StatusBadRequest)
			return
		}

		// Check if account already exists
		var existing models.Account
		if err := database.Where("email = ? AND provider = ?", email, "google").First(&existing).Error; err == nil {
			// Update existing account
			existing.AccessToken = cred.AccessToken
			existing.RefreshToken = cred.RefreshToken
			existing.ExpiresAt = cred.ExpiresAt
			existing.UpdatedAt = time.Now()
			if cred.ProjectID != "" {
				existing.Metadata = `{"project_id":"` + cred.ProjectID + `","source":"` + cred.Source + `"}`
			}
			database.Save(&existing)

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"message": "Account updated",
				"account_id": existing.ID,
			})
			return
		}

		// Create new account
		account := models.Account{
			ID:           uuid.New().String(),
			Email:        email,
			Provider:     "google",
			AccessToken:  cred.AccessToken,
			RefreshToken: cred.RefreshToken,
			ExpiresAt:    cred.ExpiresAt,
			IsActive:     true,
			IsPrimary:    false,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		if cred.ProjectID != "" {
			account.Metadata = `{"project_id":"` + cred.ProjectID + `","source":"` + cred.Source + `"}`
		}

		if err := database.Create(&account).Error; err != nil {
			http.Error(w, `{"error": "Failed to create account: `+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Account imported",
			"account_id": account.ID,
		})
	}
}
