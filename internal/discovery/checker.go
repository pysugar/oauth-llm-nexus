package discovery

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "github.com/glebarez/go-sqlite"
)

// IDEConfig represents the current configuration of an AI IDE
type IDEConfig struct {
	IDE         string            `json:"ide"`          // e.g., "Claude Code", "Codex", "Gemini CLI"
	ConfigPath  string            `json:"config_path"`  // Path to config file
	Exists      bool              `json:"exists"`       // Whether config file exists
	RawContent  string            `json:"raw_content"`  // Full raw content of config file
	BaseURL     string            `json:"base_url"`     // Parsed base URL
	APIKey      string            `json:"api_key"`      // Masked API key
	Model       string            `json:"model"`        // Current model setting
	EnvVars     map[string]string `json:"env_vars"`     // All parsed env vars (masked)
	Extra       map[string]string `json:"extra"`        // Additional parsed info
	ConfigFiles []ConfigFile      `json:"config_files"` // Multiple config files (for Codex, etc.)
}

// ConfigFile represents a single config file
type ConfigFile struct {
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	RawContent string `json:"raw_content"`
	Format     string `json:"format"` // json, toml, env
}

// MCPServer represents an MCP server configuration
type MCPServer struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Command  string                 `json:"command"`
	Args     []string               `json:"args"`
	Env      map[string]string      `json:"env"`
	RawValue map[string]interface{} `json:"raw_value"`
}

// PromptFile represents a prompt file
type PromptFile struct {
	IDE        string `json:"ide"`
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	Content    string `json:"content"`
	LineCount  int    `json:"line_count"`
	CharCount  int    `json:"char_count"`
}

// SkillInfo represents an installed skill
type SkillInfo struct {
	Name        string `json:"name"`
	Directory   string `json:"directory"`
	Description string `json:"description"`
}

// FullConfigReport represents the complete config inspection result
type FullConfigReport struct {
	IDEConfigs []IDEConfig  `json:"ide_configs"`
	MCPServers []MCPServer  `json:"mcp_servers"`
	Prompts    []PromptFile `json:"prompts"`
	Skills     []SkillInfo  `json:"skills"`
}

// CheckAllConfigs performs a full config inspection
func CheckAllConfigs() *FullConfigReport {
	report := &FullConfigReport{
		IDEConfigs: []IDEConfig{},
		MCPServers: []MCPServer{},
		Prompts:    []PromptFile{},
		Skills:     []SkillInfo{},
	}

	// Check IDE configs
	report.IDEConfigs = append(report.IDEConfigs, checkClaudeCode())
	report.IDEConfigs = append(report.IDEConfigs, checkCodex())
	report.IDEConfigs = append(report.IDEConfigs, checkGeminiCLI())
	report.IDEConfigs = append(report.IDEConfigs, checkZed())
	report.IDEConfigs = append(report.IDEConfigs, checkAlma())
	report.IDEConfigs = append(report.IDEConfigs, checkCCSwitch())
	report.IDEConfigs = append(report.IDEConfigs, checkAntigravity())
	report.IDEConfigs = append(report.IDEConfigs, checkCursor())
	report.IDEConfigs = append(report.IDEConfigs, checkWindsurf())
	report.IDEConfigs = append(report.IDEConfigs, checkCline())

	// Check MCP servers
	report.MCPServers = checkAllMCPServers()

	// Check prompts
	report.Prompts = checkAllPrompts()

	// Check skills
	report.Skills = checkAllSkills()

	return report
}

// maskAPIKey masks an API key for display
func maskAPIKey(key string) string {
	if len(key) <= 8 {
		if len(key) > 0 {
			return key[:1] + "***"
		}
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// checkClaudeCode checks Claude Code configuration
func checkClaudeCode() IDEConfig {
	config := IDEConfig{
		IDE:        "Claude Code",
		ConfigPath: "~/.claude/settings.json",
		EnvVars:    make(map[string]string),
		Extra:      make(map[string]string),
	}

	path := expandPath("~/.claude/settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		config.Exists = false
		return config
	}

	config.Exists = true
	config.RawContent = string(data)

	var settings struct {
		Env   map[string]string `json:"env"`
		Model string            `json:"model"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return config
	}

	// Extract and mask env vars
	for k, v := range settings.Env {
		if strings.Contains(strings.ToLower(k), "token") ||
			strings.Contains(strings.ToLower(k), "key") ||
			strings.Contains(strings.ToLower(k), "secret") {
			config.EnvVars[k] = maskAPIKey(v)
		} else {
			config.EnvVars[k] = v
		}

		// Extract specific fields
		switch k {
		case "ANTHROPIC_BASE_URL":
			config.BaseURL = v
		case "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY":
			config.APIKey = maskAPIKey(v)
		case "ANTHROPIC_MODEL":
			config.Model = v
		}
	}

	if settings.Model != "" && config.Model == "" {
		config.Model = settings.Model
	}

	return config
}

// checkCodex checks Codex configuration (dual file)
func checkCodex() IDEConfig {
	config := IDEConfig{
		IDE:         "Codex",
		ConfigPath:  "~/.codex/",
		EnvVars:     make(map[string]string),
		Extra:       make(map[string]string),
		ConfigFiles: []ConfigFile{},
	}

	// Check auth.json
	authPath := expandPath("~/.codex/auth.json")
	authFile := ConfigFile{
		Path:   "~/.codex/auth.json",
		Format: "json",
	}
	if authData, err := os.ReadFile(authPath); err == nil {
		authFile.Exists = true
		authFile.RawContent = string(authData)
		config.Exists = true

		var auth map[string]string
		if err := json.Unmarshal(authData, &auth); err == nil {
			if key, ok := auth["OPENAI_API_KEY"]; ok {
				config.APIKey = maskAPIKey(key)
				config.EnvVars["OPENAI_API_KEY"] = maskAPIKey(key)
			}
		}
	}
	config.ConfigFiles = append(config.ConfigFiles, authFile)

	// Check config.toml
	tomlPath := expandPath("~/.codex/config.toml")
	tomlFile := ConfigFile{
		Path:   "~/.codex/config.toml",
		Format: "toml",
	}
	if tomlData, err := os.ReadFile(tomlPath); err == nil {
		tomlFile.Exists = true
		tomlFile.RawContent = string(tomlData)
		config.Exists = true

		// Simple TOML parsing
		lines := strings.Split(string(tomlData), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || line == "" {
				continue
			}

			if strings.HasPrefix(line, "model_provider") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					config.Extra["model_provider"] = strings.Trim(strings.TrimSpace(parts[1]), "\"'")
				}
			} else if strings.HasPrefix(line, "model ") || strings.HasPrefix(line, "model=") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					config.Model = strings.Trim(strings.TrimSpace(parts[1]), "\"'")
				}
			} else if strings.Contains(line, "base_url") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					config.BaseURL = strings.Trim(strings.TrimSpace(parts[1]), "\"'")
				}
			}
		}
	}
	config.ConfigFiles = append(config.ConfigFiles, tomlFile)

	return config
}

// checkGeminiCLI checks Gemini CLI configuration
func checkGeminiCLI() IDEConfig {
	config := IDEConfig{
		IDE:         "Gemini CLI",
		ConfigPath:  "~/.gemini/",
		EnvVars:     make(map[string]string),
		Extra:       make(map[string]string),
		ConfigFiles: []ConfigFile{},
	}

	// Check .env
	envPath := expandPath("~/.gemini/.env")
	envFile := ConfigFile{
		Path:   "~/.gemini/.env",
		Format: "env",
	}
	if envData, err := os.ReadFile(envPath); err == nil {
		envFile.Exists = true
		envFile.RawContent = string(envData)
		config.Exists = true

		lines := strings.Split(string(envData), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || line == "" {
				continue
			}

			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}

			key := strings.TrimSpace(parts[0])
			value := strings.Trim(strings.TrimSpace(parts[1]), "\"'")

			if strings.Contains(strings.ToLower(key), "key") ||
				strings.Contains(strings.ToLower(key), "token") {
				config.EnvVars[key] = maskAPIKey(value)
			} else {
				config.EnvVars[key] = value
			}

			switch key {
			case "GEMINI_API_KEY", "GOOGLE_GEMINI_API_KEY":
				config.APIKey = maskAPIKey(value)
			case "GOOGLE_GEMINI_BASE_URL":
				config.BaseURL = value
			case "GEMINI_MODEL":
				config.Model = value
			}
		}
	}
	config.ConfigFiles = append(config.ConfigFiles, envFile)

	// Check settings.json
	settingsPath := expandPath("~/.gemini/settings.json")
	settingsFile := ConfigFile{
		Path:   "~/.gemini/settings.json",
		Format: "json",
	}
	if settingsData, err := os.ReadFile(settingsPath); err == nil {
		settingsFile.Exists = true
		settingsFile.RawContent = string(settingsData)
		config.Exists = true
	}
	config.ConfigFiles = append(config.ConfigFiles, settingsFile)

	// Check google_accounts.json (OAuth accounts)
	googleAccountsPath := expandPath("~/.gemini/google_accounts.json")
	googleAccountsFile := ConfigFile{
		Path:   "~/.gemini/google_accounts.json",
		Format: "json",
	}
	if data, err := os.ReadFile(googleAccountsPath); err == nil {
		googleAccountsFile.Exists = true
		googleAccountsFile.RawContent = string(data)
		config.Exists = true

		var accounts []map[string]interface{}
		if err := json.Unmarshal(data, &accounts); err == nil {
			config.Extra["google_accounts"] = strconv.Itoa(len(accounts))
		}
	}
	config.ConfigFiles = append(config.ConfigFiles, googleAccountsFile)

	// Check oauth_creds.json
	oauthCredsPath := expandPath("~/.gemini/oauth_creds.json")
	oauthCredsFile := ConfigFile{
		Path:   "~/.gemini/oauth_creds.json",
		Format: "json",
	}
	if _, err := os.ReadFile(oauthCredsPath); err == nil {
		oauthCredsFile.Exists = true
		// Mask sensitive OAuth data
		oauthCredsFile.RawContent = "(OAuth credentials - exists)"
		config.Exists = true
	}
	config.ConfigFiles = append(config.ConfigFiles, oauthCredsFile)

	return config
}

// checkCCSwitch checks CC-Switch configuration and content from SQLite DB
func checkCCSwitch() IDEConfig {
	config := IDEConfig{
		IDE:         "CC-Switch",
		ConfigPath:  "~/.cc-switch/settings.json",
		EnvVars:     make(map[string]string),
		Extra:       make(map[string]string),
		ConfigFiles: []ConfigFile{},
	}

	// 1. Check settings.json
	path := expandPath("~/.cc-switch/settings.json")
	if data, err := os.ReadFile(path); err == nil {
		config.Exists = true
		config.RawContent = string(data)
		config.ConfigFiles = append(config.ConfigFiles, ConfigFile{
			Path:       "~/.cc-switch/settings.json",
			Exists:     true,
			RawContent: string(data),
			Format:     "json",
		})

		var settings map[string]interface{}
		if err := json.Unmarshal(data, &settings); err == nil {
			if lang, ok := settings["language"].(string); ok {
				config.Extra["language"] = lang
			}
		}
	}

	// 2. Check cc-switch.db
	dbPath := expandPath("~/.cc-switch/cc-switch.db")
	if _, err := os.Stat(dbPath); err == nil {
		config.Exists = true
		config.Extra["database_path"] = "~/.cc-switch/cc-switch.db"

		db, err := sql.Open("sqlite", dbPath)
		if err == nil {
			defer db.Close()

			// Count providers
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM providers").Scan(&count); err == nil {
				config.Extra["provider_count"] = strconv.Itoa(count)
			}

			// Get provider names and types
			rows, err := db.Query("SELECT name, app_type FROM providers")
			if err == nil {
				defer rows.Close()
				var providerList []string
				for rows.Next() {
					var name, appType string
					if err := rows.Scan(&name, &appType); err == nil {
						providerList = append(providerList, name+" ("+appType+")")
					}
				}
				if len(providerList) > 0 {
					config.Extra["providers"] = strings.Join(providerList, ", ")
				}
			}
		}
	}

	return config
}

// checkZed checks Zed editor configuration
func checkZed() IDEConfig {
	config := IDEConfig{
		IDE:        "Zed",
		ConfigPath: "~/.config/zed/settings.json",
		EnvVars:    make(map[string]string),
		Extra:      make(map[string]string),
	}

	path := expandPath("~/.config/zed/settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		config.Exists = false
		return config
	}

	config.Exists = true
	config.RawContent = string(data)

	// Parse language_models section
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err == nil {
		if lm, ok := settings["language_models"].(map[string]interface{}); ok {
			// Check for OpenAI compatible settings
			if openai, ok := lm["openai"].(map[string]interface{}); ok {
				if apiURL, ok := openai["api_url"].(string); ok {
					config.BaseURL = apiURL
				}
				if apiKey, ok := openai["api_key"].(string); ok {
					config.APIKey = maskAPIKey(apiKey)
				}
			}
			// Check for Anthropic settings
			if anthropic, ok := lm["anthropic"].(map[string]interface{}); ok {
				if apiKey, ok := anthropic["api_key"].(string); ok {
					config.APIKey = maskAPIKey(apiKey)
					config.EnvVars["anthropic_api_key"] = maskAPIKey(apiKey)
				}
			}
		}
	}

	return config
}

// checkAlma checks Alma AI CLI configuration
func checkAlma() IDEConfig {
	config := IDEConfig{
		IDE:        "Alma",
		ConfigPath: "~/.config/alma/",
		EnvVars:    make(map[string]string),
		Extra:      make(map[string]string),
		ConfigFiles: []ConfigFile{},
	}

	// Check ~/.config/alma/config.json
	configPath := expandPath("~/.config/alma/config.json")
	configFile := ConfigFile{
		Path:   "~/.config/alma/config.json",
		Format: "json",
	}
	if data, err := os.ReadFile(configPath); err == nil {
		configFile.Exists = true
		configFile.RawContent = string(data)
		config.Exists = true

		var almaConfig map[string]interface{}
		if err := json.Unmarshal(data, &almaConfig); err == nil {
			if provider, ok := almaConfig["provider"].(string); ok {
				config.Extra["provider"] = provider
			}
			if model, ok := almaConfig["model"].(string); ok {
				config.Model = model
			}
			if apiKey, ok := almaConfig["api_key"].(string); ok {
				config.APIKey = maskAPIKey(apiKey)
			}
		}
	}
	config.ConfigFiles = append(config.ConfigFiles, configFile)

	return config
}

// checkAntigravity checks Antigravity configuration
func checkAntigravity() IDEConfig {
	config := IDEConfig{
		IDE:         "Antigravity",
		ConfigPath:  "~/.antigravity_tools/",
		EnvVars:     make(map[string]string),
		Extra:       make(map[string]string),
		ConfigFiles: []ConfigFile{},
	}

	// Check accounts.json (contains non-sensitive account list)
	accountsPath := expandPath("~/.antigravity_tools/accounts.json")
	accountsFile := ConfigFile{
		Path:   "~/.antigravity_tools/accounts.json",
		Format: "json",
	}
	if data, err := os.ReadFile(accountsPath); err == nil {
		accountsFile.Exists = true
		accountsFile.RawContent = string(data) // Show full content (non-sensitive)
		config.Exists = true

		var accounts []map[string]interface{}
		if err := json.Unmarshal(data, &accounts); err == nil {
			config.Extra["account_count"] = strconv.Itoa(len(accounts))
		}
	}
	config.ConfigFiles = append(config.ConfigFiles, accountsFile)

	// Check individual account files and list them
	accountsDir := expandPath("~/.antigravity_tools/accounts")
	if entries, err := os.ReadDir(accountsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}

			accountPath := filepath.Join(accountsDir, entry.Name())
			accountFile := ConfigFile{
				Path:   "~/.antigravity_tools/accounts/" + entry.Name(),
				Format: "json",
			}

			if data, err := os.ReadFile(accountPath); err == nil {
				accountFile.Exists = true
				config.Exists = true

				// Extract email for display, mask other sensitive data
				var account map[string]interface{}
				if err := json.Unmarshal(data, &account); err == nil {
					// Show only non-sensitive fields
					safeInfo := make(map[string]interface{})
					if email, ok := account["email"].(string); ok {
						safeInfo["email"] = email
					}
					if uuid, ok := account["uuid"].(string); ok {
						safeInfo["uuid"] = uuid
					}
					if expiry, ok := account["expiry_ts"].(float64); ok {
						safeInfo["expiry_ts"] = expiry
					}
					if safeData, err := json.MarshalIndent(safeInfo, "", "  "); err == nil {
						accountFile.RawContent = string(safeData)
					}
				}
			}

			config.ConfigFiles = append(config.ConfigFiles, accountFile)
		}
	}

	return config
}

// checkCursor checks Cursor configuration
func checkCursor() IDEConfig {
	return checkGenericIDE("Cursor", "~/.cursor/settings.json")
}

// checkWindsurf checks Windsurf configuration
func checkWindsurf() IDEConfig {
	return checkGenericIDE("Windsurf", "~/.windsurf/settings.json")
}

// checkCline checks Cline configuration
func checkCline() IDEConfig {
	return checkGenericIDE("Cline", "~/.cline/settings.json")
}

// checkGenericIDE checks a generic IDE with settings.json
func checkGenericIDE(name, configPath string) IDEConfig {
	config := IDEConfig{
		IDE:        name,
		ConfigPath: configPath,
		EnvVars:    make(map[string]string),
		Extra:      make(map[string]string),
	}

	path := expandPath(configPath)
	data, err := os.ReadFile(path)
	if err != nil {
		config.Exists = false
		return config
	}

	config.Exists = true
	config.RawContent = string(data)

	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err == nil {
		for k, v := range settings.Env {
			if strings.Contains(strings.ToLower(k), "token") ||
				strings.Contains(strings.ToLower(k), "key") {
				config.EnvVars[k] = maskAPIKey(v)
				if strings.Contains(strings.ToLower(k), "key") {
					config.APIKey = maskAPIKey(v)
				}
			} else {
				config.EnvVars[k] = v
			}

			if strings.Contains(strings.ToLower(k), "base_url") {
				config.BaseURL = v
			}
		}
	}

	return config
}

// checkAllMCPServers checks all MCP server configurations
func checkAllMCPServers() []MCPServer {
	servers := []MCPServer{}

	// Claude MCP: ~/.claude.json
	claudeMCPPath := expandPath("~/.claude.json")
	if data, err := os.ReadFile(claudeMCPPath); err == nil {
		var claudeConfig struct {
			MCPServers map[string]map[string]interface{} `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &claudeConfig); err == nil {
			for id, server := range claudeConfig.MCPServers {
				mcp := MCPServer{
					ID:       id,
					Name:     id,
					RawValue: server,
				}
				if cmd, ok := server["command"].(string); ok {
					mcp.Command = cmd
				}
				if args, ok := server["args"].([]interface{}); ok {
					for _, arg := range args {
						if s, ok := arg.(string); ok {
							mcp.Args = append(mcp.Args, s)
						}
					}
				}
				servers = append(servers, mcp)
			}
		}
	}

	// Gemini MCP: ~/.gemini/settings.json
	geminiMCPPath := expandPath("~/.gemini/settings.json")
	if data, err := os.ReadFile(geminiMCPPath); err == nil {
		var geminiConfig struct {
			MCPServers map[string]map[string]interface{} `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &geminiConfig); err == nil {
			for id, server := range geminiConfig.MCPServers {
				mcp := MCPServer{
					ID:       id,
					Name:     id + " (Gemini)",
					RawValue: server,
				}
				if cmd, ok := server["command"].(string); ok {
					mcp.Command = cmd
				}
				servers = append(servers, mcp)
			}
		}
	}

	return servers
}

// checkAllPrompts checks all prompt files
func checkAllPrompts() []PromptFile {
	prompts := []PromptFile{}

	promptPaths := map[string]string{
		"Claude": "~/.claude/CLAUDE.md",
		"Codex":  "~/.codex/AGENTS.md",
		"Gemini": "~/.gemini/GEMINI.md",
	}

	for ide, path := range promptPaths {
		prompt := PromptFile{
			IDE:  ide,
			Path: path,
		}

		fullPath := expandPath(path)
		if data, err := os.ReadFile(fullPath); err == nil {
			prompt.Exists = true
			content := string(data)
			prompt.Content = content
			prompt.CharCount = len(content)
			prompt.LineCount = strings.Count(content, "\n") + 1
		}

		prompts = append(prompts, prompt)
	}

	return prompts
}

// checkAllSkills checks all installed skills
func checkAllSkills() []SkillInfo {
	skills := []SkillInfo{}

	skillDirs := []string{
		"~/.claude/skills",
		"~/.codex/skills",
		"~/.gemini/skills",
	}

	for _, dir := range skillDirs {
		fullPath := expandPath(dir)
		entries, err := os.ReadDir(fullPath)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			skillPath := filepath.Join(fullPath, entry.Name())
			skillMD := filepath.Join(skillPath, "SKILL.md")

			skill := SkillInfo{
				Name:      entry.Name(),
				Directory: skillPath,
			}

			// Try to parse SKILL.md for description
			if data, err := os.ReadFile(skillMD); err == nil {
				content := string(data)
				// Simple extraction of name from frontmatter
				if strings.Contains(content, "---") {
					parts := strings.SplitN(content, "---", 3)
					if len(parts) >= 3 {
						frontmatter := parts[1]
						for _, line := range strings.Split(frontmatter, "\n") {
							if strings.HasPrefix(line, "name:") {
								skill.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
							} else if strings.HasPrefix(line, "description:") {
								skill.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
							}
						}
					}
				}
			}

			skills = append(skills, skill)
		}
	}

	return skills
}
