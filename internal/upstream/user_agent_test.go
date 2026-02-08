package upstream

import "testing"

func TestConfiguredUserAgent_Default(t *testing.T) {
	t.Setenv("NEXUS_ANTIGRAVITY_USER_AGENT", "")
	got := configuredUserAgent()
	if got != DefaultUserAgent {
		t.Fatalf("expected default user agent %q, got %q", DefaultUserAgent, got)
	}
}

func TestConfiguredUserAgent_FromPrimaryEnv(t *testing.T) {
	want := "antigravity/9.9.9 linux/amd64"
	t.Setenv("NEXUS_ANTIGRAVITY_USER_AGENT", want)
	got := configuredUserAgent()
	if got != want {
		t.Fatalf("expected env user agent %q, got %q", want, got)
	}
}
