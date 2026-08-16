package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// #1926: seven `_`-prefixed test-fixture profiles were listed inline with the
// user's real ones, burying them. They are separated, not hidden — a profile
// someone deliberately named with a leading underscore must still appear.
func TestIssue1926_ProfileListSeparatesInternal(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()

	for _, name := range []string{"work", "_test-1926", "_scratch"} {
		if _, stderr, code := runAgentDeck(t, home, "profile", "create", name); code != 0 {
			t.Fatalf("profile create %s failed (exit %d): %s", name, code, stderr)
		}
	}

	stdout, stderr, code := runAgentDeck(t, home, "profile", "list")
	if code != 0 {
		t.Fatalf("profile list failed (exit %d): %s", code, stderr)
	}

	// Nothing may disappear: hiding by default would make an unexpected
	// underscore profile invisible, which is worse than the clutter.
	for _, name := range []string{"work", "_test-1926", "_scratch"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("profile %q missing from listing:\n%s", name, stdout)
		}
	}
	if !strings.Contains(stdout, "Internal") {
		t.Errorf("listing does not separate internal profiles:\n%s", stdout)
	}

	// The real profile must come before the internal section, which is the
	// entire point — otherwise it is still buried.
	realIdx := strings.Index(stdout, "work")
	sectionIdx := strings.Index(stdout, "Internal")
	if realIdx < 0 || sectionIdx < 0 || realIdx > sectionIdx {
		t.Errorf("real profiles should be listed before the internal section:\n%s", stdout)
	}
}

// The JSON payload gains an `internal` flag so tooling can filter without
// re-deriving the convention. Additive: existing keys are untouched.
func TestIssue1926_ProfileListJSONMarksInternal(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	for _, name := range []string{"work", "_test-1926"} {
		if _, stderr, code := runAgentDeck(t, home, "profile", "create", name); code != 0 {
			t.Fatalf("profile create %s failed (exit %d): %s", name, code, stderr)
		}
	}

	stdout, stderr, code := runAgentDeck(t, home, "profile", "list", "--json")
	if code != 0 {
		t.Fatalf("profile list --json failed (exit %d): %s", code, stderr)
	}
	var resp struct {
		Profiles []struct {
			Name      string `json:"name"`
			IsDefault bool   `json:"is_default"`
			Internal  bool   `json:"internal"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse: %v\n%s", err, stdout)
	}

	seen := map[string]bool{}
	for _, p := range resp.Profiles {
		seen[p.Name] = true
		want := strings.HasPrefix(p.Name, "_")
		if p.Internal != want {
			t.Errorf("profile %q: internal = %v, want %v", p.Name, p.Internal, want)
		}
	}
	for _, name := range []string{"work", "_test-1926"} {
		if !seen[name] {
			t.Errorf("profile %q missing from JSON payload:\n%s", name, stdout)
		}
	}
}
