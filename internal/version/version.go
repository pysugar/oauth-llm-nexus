package version

// These variables are set at build time via -ldflags
// Example: go build -ldflags "-X github.com/pysugar/oauth-llm-nexus/internal/version.Version=v0.1.5"
var (
	// Version is the semantic version of the application
	Version = "dev"

	// Commit is the git commit hash
	Commit = "none"

	// BuildTime is the timestamp of the build
	BuildTime = "unknown"
)
