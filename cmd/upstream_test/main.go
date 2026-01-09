// upstream_test - Direct upstream API testing tool
// Tests various models directly against Cloud Code API endpoints
// Fully simulates Antigravity/CLIProxyAPI request format including loadCodeAssist

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Antigravity identity prompt (from CLIProxyAPI commit 67985d8 - full version with XML tags)
const antigravityIdentity = `<identity>
You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.
You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.
The USER will send you requests, which you must always prioritize addressing. Along with each USER request, we will attach additional metadata about their current state, such as what files they have open and where their cursor is.
This information may or may not be relevant to the coding task, it is up for you to decide.
</identity>

<tool_calling>
Call tools as you normally would. The following list provides additional guidance to help you avoid errors:
 - **Absolute paths only**. When using tools that accept file path arguments, ALWAYS use the absolute file path.
</tool_calling>

<web_application_development>
## Technology Stack,
Your web applications should be built using the following technologies:,
1. **Core**: Use HTML for structure and Javascript for logic.
2. **Styling (CSS)**: Use Vanilla CSS for maximum flexibility and control. Avoid using TailwindCSS unless the USER explicitly requests it; in this case, first confirm which TailwindCSS version to use.
3. **Web App**: If the USER specifies that they want a more complex web app, use a framework like Next.js or Vite. Only do this if the USER explicitly requests a web app.
4. **New Project Creation**: If you need to use a framework for a new app, use npx with the appropriate script, but there are some rules to follow:,
 - Use npx -y to automatically install the script and its dependencies
 - You MUST run the command with --help flag to see all available options first, 
 - Initialize the app in the current directory with ./ (example: npx -y create-vite-app@latest ./),
 - You should run in non-interactive mode so that the user doesn't need to input anything,
5. **Running Locally**: When running locally, use npm run dev or equivalent dev server. Only build the production bundle if the USER explicitly requests it or you are validating the code for correctness.

# Design Aesthetics,
1. **Use Rich Aesthetics**: The USER should be wowed at first glance by the design. Use best practices in modern web design (e.g. vibrant colors, dark modes, glassmorphism, and dynamic animations) to create a stunning first impression. Failure to do this is UNACCEPTABLE.
2. **Prioritize Visual Excellence**: Implement designs that will WOW the user and feel extremely premium:
		- Avoid generic colors (plain red, blue, green). Use curated, harmonious color palettes (e.g., HSL tailored colors, sleek dark modes).
 - Using modern typography (e.g., from Google Fonts like Inter, Roboto, or Outfit) instead of browser defaults.
		- Use smooth gradients,
		- Add subtle micro-animations for enhanced user experience,
3. **Use a Dynamic Design**: An interface that feels responsive and alive encourages interaction. Achieve this with hover effects and interactive elements. Micro-animations, in particular, are highly effective for improving user engagement.
4. **Premium Designs**. Make a design that feels premium and state of the art. Avoid creating simple minimum viable products.
4. **Don't use placeholders**. If you need an image, use your generate_image tool to create a working demonstration.,

## Implementation Workflow,
Follow this systematic approach when building web applications:,
1. **Plan and Understand**:,
		- Fully understand the user's requirements,
		- Draw inspiration from modern, beautiful, and dynamic web designs,
		- Outline the features needed for the initial version,
2. **Build the Foundation**:,
		- Start by creating/modifying index.css,
		- Implement the core design system with all tokens and utilities,
3. **Create Components**:,
		- Build necessary components using your design system,
		- Ensure all components use predefined styles, not ad-hoc utilities,
		- Keep components focused and reusable,
4. **Assemble Pages**:,
		- Update the main application to incorporate your design and components,
		- Ensure proper routing and navigation,
		- Implement responsive layouts,
5. **Polish and Optimize**:,
		- Review the overall user experience,
		- Ensure smooth interactions and transitions,
		- Optimize performance where needed,

## SEO Best Practices,
Automatically implement SEO best practices on every page:,
- **Title Tags**: Include proper, descriptive title tags for each page,
- **Meta Descriptions**: Add compelling meta descriptions that accurately summarize page content,
- **Heading Structure**: Use a single h1 per page with proper heading hierarchy,
- **Semantic HTML**: Use appropriate HTML5 semantic elements,
- **Unique IDs**: Ensure all interactive elements have unique, descriptive IDs for browser testing,
- **Performance**: Ensure fast page load times through optimization,
CRITICAL REMINDER: AESTHETICS ARE VERY IMPORTANT. If your web app looks simple and basic then you have FAILED!
</web_application_development>
<ephemeral_message>
There will be an <EPHEMERAL_MESSAGE> appearing in the conversation at times. This is not coming from the user, but instead injected by the system as important information to pay attention to. 
Do not respond to nor acknowledge those messages, but do follow them strictly.
</ephemeral_message>


<communication_style>
- **Formatting**. Format your responses in github-style markdown to make your responses easier for the USER to parse. For example, use headers to organize your responses and bolded or italicized text to highlight important keywords. Use backticks to format file, directory, function, and class names. If providing a URL to the user, format this in markdown as well, for example [label](example.com).
- **Proactiveness**. As an agent, you are allowed to be proactive, but only in the course of completing the user's task. For example, if the user asks you to add a new component, you can edit the code, verify build and test statuses, and take any other obvious follow-up actions, such as performing additional research. However, avoid surprising the user. For example, if the user asks HOW to approach something, you should answer their question and instead of jumping into editing a file.
- **Helpfulness**. Respond like a helpful software engineer who is explaining your work to a friendly collaborator on the project. Acknowledge mistakes or any backtracking you do as a result of new information.
- **Ask for clarification**. If you are unsure about the USER's intent, always ask for clarification rather than making assumptions.
</communication_style>`

// Test models to check
var testModels = []struct {
	Name        string
	Category    string // gemini, claude, oss
	RequestType string // "agent" or "web_search"
}{
	// Gemini models
	{"gemini-2.5-flash", "gemini", "agent"},
	{"gemini-3-flash", "gemini", "agent"},
	{"gemini-3-pro-high", "gemini", "agent"},

	// Claude models
	{"claude-sonnet-4-5", "claude", "agent"},
	{"claude-sonnet-4-5-thinking", "claude", "agent"},
	{"claude-opus-4-5-thinking", "claude", "agent"},

	// OSS models
	{"gpt-oss-120b-medium", "oss", "agent"},
}

// Endpoints to test (matching CLIProxyAPI: daily -> prod -> sandbox)
// Updated to match oauth-llm-nexus implementation
var endpoints = []string{
	"https://daily-cloudcode-pa.googleapis.com/v1internal",         // daily
	"https://cloudcode-pa.googleapis.com/v1internal",               // prod
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal", // sandbox daily
}

func main() {
	log.SetFlags(log.Ltime)

	// Find nexus.db
	dbPath := findNexusDB()
	if dbPath == "" {
		log.Fatal("❌ Cannot find nexus.db")
	}
	log.Printf("📂 Using database: %s", dbPath)

	// Open database
	gormDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatalf("❌ Failed to open DB: %v", err)
	}

	// Get accounts
	var accounts []models.Account
	if err := gormDB.Where("is_active = ?", true).Find(&accounts).Error; err != nil || len(accounts) == 0 {
		log.Fatal("❌ No active accounts found in database")
	}

	var account *models.Account
	for i := range accounts {
		if accounts[i].IsPrimary && accounts[i].IsActive {
			account = &accounts[i]
			break
		}
	}
	if account == nil {
		account = &accounts[0]
	}

	log.Printf("👤 Using account: %s", account.Email)
	log.Printf("🔑 Token expires: %s", account.ExpiresAt.Format(time.RFC3339))

	// Check if token is expired
	if time.Now().After(account.ExpiresAt) {
		log.Printf("⚠️  WARNING: Access token appears to be EXPIRED!")
	}

	// Extract project ID from metadata
	var metadata map[string]string
	if err := json.Unmarshal([]byte(account.Metadata), &metadata); err != nil {
		log.Fatalf("❌ Failed to parse metadata: %v", err)
	}
	projectID := metadata["project_id"]

	// Try to fetch project ID via loadCodeAssist if not already set
	if projectID == "" {
		log.Printf("🔍 Project ID not found in metadata, calling loadCodeAssist...")
		fetchedProjectID, err := loadCodeAssist(account.AccessToken)
		if err != nil {
			log.Printf("⚠️  loadCodeAssist failed: %v", err)
			projectID = "rising-fact-p41fc" // updated fallback
		} else {
			projectID = fetchedProjectID
			log.Printf("✅ Obtained project ID via loadCodeAssist: %s", projectID)
		}
	} else {
		log.Printf("🏗️  Project ID (from metadata): %s", projectID)
	}

	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Println("UPSTREAM API DIRECT TEST (Full CLIProxyAPI Simulation)")
	fmt.Println(strings.Repeat("=", 80))

	// Test each model
	results := make(map[string]map[string]string) // model -> endpoint -> result

	for _, model := range testModels {
		results[model.Name] = make(map[string]string)
		fmt.Printf("\n📦 Testing model: %s (%s)\n", model.Name, model.Category)
		fmt.Println(strings.Repeat("-", 60))

		for _, endpoint := range endpoints {
			endpointName := getEndpointName(endpoint)
			status, msg, retryAfter := testModel(endpoint, model.Name, model.RequestType, projectID, account.AccessToken)
			results[model.Name][endpointName] = fmt.Sprintf("%d", status)

			icon := "❌"
			if status == 200 {
				icon = "✅"
			} else if status == 403 {
				icon = "🔒"
			} else if status == 429 {
				icon = "⏳"
			}

			extraInfo := ""
			if retryAfter != nil {
				extraInfo = fmt.Sprintf(" (retry after: %v)", *retryAfter)
			}

			fmt.Printf("  %s %-10s: %d - %s%s\n", icon, endpointName, status, truncate(msg, 70), extraInfo)
		}
	}

	// Print summary table
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Println("SUMMARY TABLE")
	fmt.Println(strings.Repeat("=", 80))

	// Header
	fmt.Printf("%-30s | %-12s | %-12s | %-10s\n", "Model", "sandbox", "daily", "prod")
	fmt.Println(strings.Repeat("-", 80))

	// Data rows
	for _, model := range testModels {
		sandbox := results[model.Name]["sandbox"]
		daily := results[model.Name]["daily"]
		prod := results[model.Name]["prod"]
		fmt.Printf("%-30s | %-12s | %-12s | %-10s\n", model.Name, sandbox, daily, prod)
	}

	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("\nLegend: 200=OK, 429=RateLimited, 403=Forbidden, 400=BadRequest")
}

// loadCodeAssist calls the loadCodeAssist API to get the project ID (same as CLIProxyAPI)
func loadCodeAssist(accessToken string) (string, error) {
	url := "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"

	reqBody := map[string]interface{}{
		"metadata": map[string]string{
			"ideType":    "IDE_UNSPECIFIED",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	}

	body, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}

	// Headers matching CLIProxyAPI (antigravity.go lines 393-397)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
	req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
	req.Header.Set("Client-Metadata", `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}

	// Extract cloudaicompanionProject from response using standard library
	var loadResp map[string]interface{}
	if err := json.Unmarshal(respBody, &loadResp); err != nil {
		return "", err
	}

	// Try direct string value
	if projectID, ok := loadResp["cloudaicompanionProject"].(string); ok && projectID != "" {
		return projectID, nil
	}

	// Try nested object format
	if projectMap, ok := loadResp["cloudaicompanionProject"].(map[string]interface{}); ok {
		if id, ok := projectMap["id"].(string); ok && id != "" {
			return id, nil
		}
	}

	return "", fmt.Errorf("no cloudaicompanionProject in response")
}

// testModel builds a fully Antigravity/CLIProxyAPI-compatible request
func testModel(endpoint, model, requestType, projectID, accessToken string) (int, string, *time.Duration) {
	// URL format: {base_url}:generateContent
	url := endpoint + ":generateContent"

	// Generate requestId in Antigravity format: "agent-{uuid}"
	requestID := fmt.Sprintf("agent-%s", uuid.New().String())

	// Build inner request (matching CLIProxyAPI antigravity_executor.go)
	innerRequest := map[string]interface{}{
		// systemInstruction with role: "user" (critical! from CLIProxyAPI commit 67985d8)
		"systemInstruction": map[string]interface{}{
			"role": "user",
			"parts": []map[string]interface{}{
				{"text": antigravityIdentity},
			},
		},
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "Say hi"},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 10,
		},
		// toolConfig matching CLIProxyAPI and now correctly nested in request
		"toolConfig": map[string]interface{}{
			"functionCallingConfig": map[string]interface{}{
				"mode": "VALIDATED",
			},
		},
	}

	// Build outer wrapper (matching CLIProxyAPI antigravity_executor.go geminiToAntigravity)
	payload := map[string]interface{}{
		"project":     projectID,
		"requestId":   requestID,
		"request":     innerRequest,
		"model":       model,
		"userAgent":   "antigravity",
		"requestType": requestType, // "agent" or "web_search"
	}

	body, _ := json.Marshal(payload)

	// Debug: print first request payload
	if os.Getenv("VERBOSE") == "1" {
		prettyJSON, _ := json.MarshalIndent(payload, "", "  ")
		log.Printf("📤 Request payload:\n%s", string(prettyJSON))
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, err.Error(), nil
	}

	// Headers matching CLIProxyAPI
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "antigravity/1.104.0 darwin/arm64") // Match CLIProxyAPI defaultAntigravityAgent
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error(), nil
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// Extract error message and retry-after if not 200
	if resp.StatusCode != 200 {
		var retryAfter *time.Duration

		// Parse retry-after from 429 response (matching CLIProxyAPI parseRetryDelay)
		if resp.StatusCode == 429 {
			retryAfter = parseRetryDelay(respBody)
		}

		var errResp struct {
			Error struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return resp.StatusCode, errResp.Error.Status + ": " + errResp.Error.Message, retryAfter
		}
		return resp.StatusCode, string(respBody), retryAfter
	}

	return resp.StatusCode, "OK", nil
}

// parseRetryDelay extracts the retry delay from a Google API 429 error response
// (matching CLIProxyAPI gemini_cli_executor.go parseRetryDelay) using standard library
func parseRetryDelay(errorBody []byte) *time.Duration {
	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				Type       string            `json:"@type"`
				RetryDelay string            `json:"retryDelay"`
				Metadata   map[string]string `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}

	if err := json.Unmarshal(errorBody, &errResp); err != nil {
		return nil
	}

	// Try to parse retryDelay from error.details[].RetryInfo
	for _, detail := range errResp.Error.Details {
		if detail.Type == "type.googleapis.com/google.rpc.RetryInfo" && detail.RetryDelay != "" {
			duration, err := time.ParseDuration(detail.RetryDelay)
			if err == nil {
				return &duration
			}
		}
	}

	// Fallback: try ErrorInfo.metadata.quotaResetDelay
	for _, detail := range errResp.Error.Details {
		if detail.Type == "type.googleapis.com/google.rpc.ErrorInfo" {
			if quotaResetDelay, ok := detail.Metadata["quotaResetDelay"]; ok && quotaResetDelay != "" {
				duration, err := time.ParseDuration(quotaResetDelay)
				if err == nil {
					return &duration
				}
			}
		}
	}

	// Fallback: parse from error.message "Your quota will reset after Xs."
	if errResp.Error.Message != "" {
		re := regexp.MustCompile(`after\s+(\d+)s\.?`)
		if matches := re.FindStringSubmatch(errResp.Error.Message); len(matches) > 1 {
			seconds, err := strconv.Atoi(matches[1])
			if err == nil {
				duration := time.Duration(seconds) * time.Second
				return &duration
			}
		}
	}

	return nil
}

func findNexusDB() string {
	// Check common locations
	paths := []string{
		"nexus.db",
		"../nexus.db",
		filepath.Join(os.Getenv("HOME"), ".config/oauth-llm-nexus/nexus.db"),
		filepath.Join(os.Getenv("HOME"), ".oauth-llm-nexus/nexus.db"),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}

	// Check if NEXUS_DB_PATH is set
	if envPath := os.Getenv("NEXUS_DB_PATH"); envPath != "" {
		return envPath
	}

	return ""
}

func getEndpointName(endpoint string) string {
	if strings.Contains(endpoint, "sandbox") {
		return "sandbox"
	} else if strings.Contains(endpoint, "daily") {
		return "daily"
	}
	return "prod"
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
