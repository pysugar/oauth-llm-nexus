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
	Source       string    `json:"source"`        // e.g., "antigravity", "claude", "cc-switch"
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

// Sources defines all known credential sources (inspired by cc-switch)
var Sources = []Source{
	// Antigravity Tools
	{
		Name:        "antigravity",
		Description: "Antigravity AI Tools",
		ConfigPaths: []string{
			"~/.gemini/antigravity/google_ai_credentials.json",
			"~/.antigravity_tools/credentials.json",
			"~/.antigravity_tools/accounts/*.json",
		},
		Parser: parseAntigravityCredentials,
	},
	// Claude Code / Claude Desktop
	{
		Name:        "claude",
		Description: "Claude Code / Claude Desktop",
		ConfigPaths: []string{
			"~/.claude/settings.json",
			"~/.claude.json",
		},
		Parser: parseClaudeSettings,
	},
	// CC-Switch
	{
		Name:        "cc-switch",
		Description: "CC-Switch Manager",
		ConfigPaths: []string{
			"~/.cc-switch/config.json",
			"~/.cc-switch/credentials.json",
		},
		Parser: parseCCSwitchCredentials,
	},
	// Cline
	{
		Name:        "cline",
		Description: "Cline AI Assistant",
		ConfigPaths: []string{
			"~/.cline/settings.json",
			"~/.cline/credentials.json",
		},
		Parser: parseGenericCredentials,
	},
	// Cursor
	{
		Name:        "cursor",
		Description: "Cursor Editor",
		ConfigPaths: []string{
			"~/.cursor/settings.json",
		},
		Parser: parseGenericCredentials,
	},
	// Codex
	{
		Name:        "codex",
		Description: "OpenAI Codex",
		ConfigPaths: []string{
			"~/.codex/credentials.json",
			"~/.codex/config.json",
		},
		Parser: parseGenericCredentials,
	},
	// Windsurf
	{
		Name:        "windsurf",
		Description: "Windsurf IDE",
		ConfigPaths: []string{
			"~/.windsurf/settings.json",
			"~/.windsurf/credentials.json",
		},
		Parser: parseGenericCredentials,
	},
	// Kiro
	{
		Name:        "kiro",
		Description: "Kiro AI",
		ConfigPaths: []string{
			"~/.kiro/settings.json",
			"~/.kiro/credentials.json",
		},
		Parser: parseGenericCredentials,
	},
	// Codeium
	{
		Name:        "codeium",
		Description: "Codeium",
		ConfigPaths: []string{
			"~/.codeium/credentials.json",
		},
		Parser: parseGenericCredentials,
	},
	// Gemini CLI
	{
		Name:        "gemini-cli",
		Description: "Gemini CLI",
		ConfigPaths: []string{
			"~/.config/gemini-cli/credentials.json",
			"~/.gemini-cli/credentials.json",
			"~/.gemini/oauth_creds.json",
		},
		Parser: parseGeminiOAuth,
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

	// Try legacy format first
	var creds AntigravityCredentials
	if err := json.Unmarshal(data, &creds); err == nil && creds.AccessToken != "" {
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

	// Try new account format (~/.antigravity_tools/accounts/*.json)
	var account struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Token struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresAt    int64  `json:"expiry_timestamp"`
			ProjectID    string `json:"project_id"`
		} `json:"token"`
	}
	if err := json.Unmarshal(data, &account); err == nil && account.Token.AccessToken != "" {
		return &Credential{
			Source:       "antigravity",
			Email:        account.Email,
			AccessToken:  account.Token.AccessToken,
			RefreshToken: account.Token.RefreshToken,
			ExpiresAt:    time.Unix(account.Token.ExpiresAt, 0),
			ProjectID:    account.Token.ProjectID,
			ConfigPath:   path,
		}, nil
	}

	return nil, nil
}

func parseGeminiOAuth(path string) (*Credential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Try specific Gemini oauth_creds.json format FIRST
	var creds struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiryDate   int64  `json:"expiry_date"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(data, &creds); err == nil && creds.AccessToken != "" {
		// Gemini stores expiry in milliseconds
		expiry := time.Unix(creds.ExpiryDate/1000, (creds.ExpiryDate%1000)*1000000)
		
		// Try to find email from google_accounts.json in same dir
		email := ""
		accountsPath := filepath.Join(filepath.Dir(path), "google_accounts.json")
		if accData, err := os.ReadFile(accountsPath); err == nil {
			var accs struct {
				Active string `json:"active"`
			}
			if err := json.Unmarshal(accData, &accs); err == nil {
				email = accs.Active
			}
		}

		return &Credential{
			Source:       "gemini-cli",
			Email:        email,
			AccessToken:  creds.AccessToken,
			RefreshToken: creds.RefreshToken,
			ExpiresAt:    expiry,
			ConfigPath:   path,
		}, nil
	}

	// Fallback to generic
	return parseGenericCredentials(path)
}

// ClaudeSettings represents Claude Code settings.json format
type ClaudeSettings struct {
	Env map[string]string `json:"env"`
}

func parseClaudeSettings(path string) (*Credential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var settings ClaudeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}

	// Extract token from environment variables
	authToken := settings.Env["ANTHROPIC_AUTH_TOKEN"]
	if authToken == "" {
		authToken = settings.Env["ANTHROPIC_API_KEY"]
	}

	if authToken == "" {
		return nil, nil // No credentials found
	}

	return &Credential{
		Source:      "claude",
		AccessToken: authToken,
		ConfigPath:  path,
	}, nil
}

func parseCCSwitchCredentials(path string) (*Credential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var creds map[string]interface{}
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}

	accessToken, _ := creds["access_token"].(string)
	if accessToken == "" {
		accessToken, _ = creds["token"].(string)
	}
	if accessToken == "" {
		accessToken, _ = creds["api_key"].(string)
	}
	refreshToken, _ := creds["refresh_token"].(string)
	email, _ := creds["email"].(string)

	if accessToken == "" {
		return nil, nil
	}

	return &Credential{
		Source:       "cc-switch",
		Email:        email,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ConfigPath:   path,
	}, nil
}

func parseGenericCredentials(path string) (*Credential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var creds map[string]interface{}
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}

	// Try to extract from env block first (like Claude settings.json)
	if env, ok := creds["env"].(map[string]interface{}); ok {
		for _, key := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "API_KEY"} {
			if token, ok := env[key].(string); ok && token != "" {
				return &Credential{
					Source:      filepath.Base(filepath.Dir(path)),
					AccessToken: token,
					ConfigPath:  path,
				}, nil
			}
		}
	}

	// Try direct fields
	accessToken, _ := creds["access_token"].(string)
	if accessToken == "" {
		accessToken, _ = creds["token"].(string)
	}
	if accessToken == "" {
		accessToken, _ = creds["api_key"].(string)
	}
	refreshToken, _ := creds["refresh_token"].(string)
	email, _ := creds["email"].(string)

	if accessToken == "" {
		return nil, nil
	}

	return &Credential{
		Source:       filepath.Base(filepath.Dir(path)),
		Email:        email,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ConfigPath:   path,
	}, nil
}
