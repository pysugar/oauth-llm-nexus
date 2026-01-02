package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Credential represents a discovered OAuth credential
type Credential struct {
	Source       string    `json:"source"`        // e.g., "antigravity", "gcloud"
	Email        string    `json:"email"`         // May be empty if not extractable
	AccessToken  string    `json:"access_token"`  // Will be masked in API responses
	RefreshToken string    `json:"refresh_token"` // Will be masked in API responses
	ExpiresAt    time.Time `json:"expires_at"`
	ProjectID    string    `json:"project_id"`    // Google Cloud project ID
	ConfigPath   string    `json:"config_path"`   // Original config file path
}

// Source defines a configuration source to scan
type Source struct {
	Name        string
	Description string
	ConfigPaths []string // Possible config file paths (with ~ expansion)
	Parser      func(path string) (*Credential, error)
}

// expandPath expands ~ to home directory
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// Sources defines all known credential sources
var Sources = []Source{
	{
		Name:        "antigravity",
		Description: "Antigravity AI Tools",
		ConfigPaths: []string{
			"~/.gemini/antigravity/google_ai_credentials.json",
		},
		Parser: parseAntigravityCredentials,
	},
	{
		Name:        "gemini-cli",
		Description: "Gemini CLI",
		ConfigPaths: []string{
			"~/.config/gemini-cli/credentials.json",
			"~/.gemini-cli/credentials.json",
		},
		Parser: parseGeminiCLICredentials,
	},
	{
		Name:        "codex",
		Description: "OpenAI Codex",
		ConfigPaths: []string{
			"~/.codex/credentials.json",
			"~/.config/codex/credentials.json",
		},
		Parser: parseCodexCredentials,
	},
}

// AntigravityCredentials represents the Antigravity credentials file format
type AntigravityCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	Email        string `json:"email"`
	ProjectID    string `json:"project_id"`
}

func parseAntigravityCredentials(path string) (*Credential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var creds AntigravityCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}

	return &Credential{
		Source:       "antigravity",
		Email:        creds.Email,
		AccessToken:  creds.AccessToken,
		RefreshToken: creds.RefreshToken,
		ExpiresAt:    time.Unix(creds.ExpiresAt, 0),
		ProjectID:    creds.ProjectID,
		ConfigPath:   path,
	}, nil
}

func parseGeminiCLICredentials(path string) (*Credential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var creds map[string]interface{}
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}

	accessToken, _ := creds["access_token"].(string)
	refreshToken, _ := creds["refresh_token"].(string)
	email, _ := creds["email"].(string)

	return &Credential{
		Source:       "gemini-cli",
		Email:        email,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ConfigPath:   path,
	}, nil
}

func parseCodexCredentials(path string) (*Credential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var creds map[string]interface{}
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}

	accessToken, _ := creds["access_token"].(string)
	refreshToken, _ := creds["refresh_token"].(string)
	email, _ := creds["email"].(string)

	return &Credential{
		Source:       "codex",
		Email:        email,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ConfigPath:   path,
	}, nil
}
