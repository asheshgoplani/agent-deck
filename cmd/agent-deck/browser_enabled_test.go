package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

func addProfileTestSession(t *testing.T, home, profile, title string) string {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "-p", profile, "add", t.TempDir(), "--title", title, "-c", "bash", "--no-parent", "--json")
	if code != 0 {
		t.Fatalf("add in %s: %d %s %s", profile, code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add result: %s %v", stdout, err)
	}
	return added.ID
}

func profileRequestFrames(t *testing.T, home, profile string) uint64 {
	t.Helper()
	out, errOut, code := runAgentDeck(t, home, "-p", profile, "events", "stats", "--json")
	var stats openTestStats
	if err := json.Unmarshal([]byte(out), &stats); err != nil || code != 0 {
		t.Fatalf("events stats -p %s: %d %s %s %v", profile, code, out, errOut, err)
	}
	return stats.Kinds[events.KindMacappBrowserRequest]
}

// [browser] enabled is off by default: every browser op refuses before
// anything is published, with exit 2, the setting and the command to turn it
// on, and code BROWSER_DISABLED with --json. [macapp] plugins alone is not
// enough.
func TestBrowserOffByDefaultRefuses(t *testing.T) {
	home := t.TempDir()
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n")
	id := addOpenTestSession(t, home, "Browser off")
	for _, args := range [][]string{
		{"browser", "tabs", "--session", id, "--timeout", "1s"},
		{"browser", "open", "https://example.com/", "--session", id, "--timeout", "1s"},
		{"browser", "read", "--session", id, "--timeout", "1s"},
	} {
		stdout, stderr, code := runAgentDeck(t, home, args...)
		if code != 2 || !strings.Contains(stderr, "browser.enabled") || !strings.Contains(stderr, "agent-deck config set browser.enabled true") || !strings.Contains(stderr, "agent-deck config set profiles.") || !strings.Contains(stderr, ".browser.enabled true") {
			t.Fatalf("%q: exit=%d %q %q", args, code, stdout, stderr)
		}
	}
	stdout, stderr, code := runAgentDeck(t, home, "browser", "tabs", "--session", id, "--json", "--timeout", "1s")
	var res struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Error   string `json:"error"`
	}
	if code != 2 || json.Unmarshal([]byte(stdout), &res) != nil || res.Success || res.Code != ErrCodeBrowserDisabled || !strings.Contains(res.Error, "browser.enabled") {
		t.Fatalf("--json: exit=%d %q %q", code, stdout, stderr)
	}
	if got := openTestBusDir(t, home).Kinds[events.KindMacappBrowserRequest]; got != 0 {
		t.Fatalf("a refused request was published: %d frames", got)
	}

	// On: the request is published (no app answers here: not_connected).
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n\n[browser]\nenabled = true\n")
	stdout, stderr, code = runAgentDeck(t, home, "browser", "tabs", "--session", id, "--timeout", "1s")
	if code != 3 || !strings.Contains(stderr, "not_connected") {
		t.Fatalf("enabled: exit=%d %q %q", code, stdout, stderr)
	}
	if got := openTestBusDir(t, home).Kinds[events.KindMacappBrowserRequest]; got != 1 {
		t.Fatalf("enabled request not published: %d frames", got)
	}
}

// The opt-in is per profile: [profiles.<name>.browser] enabled overrides
// [browser] enabled, both ways.
func TestBrowserEnabledPerProfile(t *testing.T) {
	home := t.TempDir()
	alpha := addProfileTestSession(t, home, "alpha", "Alpha")
	beta := addProfileTestSession(t, home, "beta", "Beta")
	run := func(profile, id string) (int, string) {
		t.Helper()
		_, stderr, code := runAgentDeck(t, home, "-p", profile, "browser", "tabs", "--session", id, "--timeout", "1s")
		return code, stderr
	}
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n\n[profiles.alpha.browser]\nenabled = true\n")
	if code, stderr := run("alpha", alpha); code != 3 {
		t.Fatalf("alpha on: exit=%d %s", code, stderr)
	}
	if code, stderr := run("beta", beta); code != 2 || !strings.Contains(stderr, `profile "beta"`) || !strings.Contains(stderr, "profiles.beta.browser.enabled true") {
		t.Fatalf("beta off: exit=%d %s", code, stderr)
	}
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n\n[browser]\nenabled = true\n\n[profiles.beta.browser]\nenabled = false\n")
	if code, stderr := run("alpha", alpha); code != 3 {
		t.Fatalf("alpha via global: exit=%d %s", code, stderr)
	}
	if code, stderr := run("beta", beta); code != 2 {
		t.Fatalf("beta off over global on: exit=%d %s", code, stderr)
	}
	if got := profileRequestFrames(t, home, "beta"); got != 0 {
		t.Fatalf("beta published %d requests", got)
	}
	if got := profileRequestFrames(t, home, "alpha"); got != 2 {
		t.Fatalf("alpha published %d requests, want 2", got)
	}
}

// The opt-in also gates every publish of macapp.browser.request: a local
// events publish, one for another profile, and one that claims to come from
// a remote. Exit 2, code BROWSER_DISABLED.
func TestEventsPublishBrowserRequestNeedsBrowserEnabled(t *testing.T) {
	home := t.TempDir()
	id := addOpenTestSession(t, home, "Publisher")
	addProfileTestSession(t, home, "alpha", "Alpha")
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n\n[profiles.alpha.browser]\nenabled = true\n")
	request := `{"v":1,"req_id":"0123456789abcdef0123456789abcdef","op":"tabs","args":{}}`
	for _, args := range [][]string{
		{"events", "publish", "--kind", events.KindMacappBrowserRequest, "--session", id, "--data", request, "--json"},
		{"events", "publish", "--kind", events.KindMacappBrowserRequest, "--session", id, "--data-file", "-", "--from-remote", "--json"},
	} {
		stdout, stderr, code := runAgentDeckStdin(t, home, request, args...)
		var res struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if code != 2 || json.Unmarshal([]byte(stdout), &res) != nil || res.Code != ErrCodeBrowserDisabled || !strings.Contains(res.Error, "agent-deck config set browser.enabled true") {
			t.Fatalf("%q: exit=%d %q %q", args, code, stdout, stderr)
		}
	}
	if got := openTestBusDir(t, home).Kinds[events.KindMacappBrowserRequest]; got != 0 {
		t.Fatalf("a refused request was published: %d", got)
	}
	// Enabled for alpha only: --profile alpha publishes.
	stdout, stderr, code := runAgentDeck(t, home, "events", "publish", "--profile", "alpha", "--kind", events.KindMacappBrowserRequest, "--session", "s", "--data", request)
	if !builtinPublishAdmitted(code, stdout+stderr) {
		t.Fatalf("alpha publish: exit=%d %q %q", code, stdout, stderr)
	}
	// The reply kinds stay ungated.
	stdout, stderr, code = runAgentDeck(t, home, "events", "publish", "--kind", events.KindMacappBrowserResponse, "--session", id, "--data", `{"v":1}`)
	if !builtinPublishAdmitted(code, stdout+stderr) {
		t.Fatalf("reply: exit=%d %q %q", code, stdout, stderr)
	}
}

// `config set` writes both forms, and `config get` reads them back.
func TestBrowserEnabledConfigKeys(t *testing.T) {
	home := t.TempDir()
	for _, c := range []struct{ key, want string }{
		{"profiles.alpha.browser.enabled", "[profiles.alpha.browser]"},
		{"browser.enabled", "[browser]"},
	} {
		if stdout, stderr, code := runAgentDeck(t, home, "config", "get", c.key); code != 0 || strings.TrimSpace(stdout) != "false" {
			t.Fatalf("get %s default: %d %q %q", c.key, code, stdout, stderr)
		}
		if stdout, stderr, code := runAgentDeck(t, home, "config", "set", c.key, "true"); code != 0 {
			t.Fatalf("set %s: %d %q %q", c.key, code, stdout, stderr)
		}
		if stdout, _, code := runAgentDeck(t, home, "config", "get", c.key); code != 0 || strings.TrimSpace(stdout) != "true" {
			t.Fatalf("get %s: %d %q", c.key, code, stdout)
		}
		raw, _ := os.ReadFile(filepath.Join(home, ".config", "agent-deck", "config.toml"))
		if !strings.Contains(string(raw), c.want) {
			t.Fatalf("config.toml lacks %s:\n%s", c.want, raw)
		}
	}
	for _, bad := range []string{"profiles..browser.enabled", "profiles.a/b.browser.enabled", "profiles.a.b.browser.enabled"} {
		if _, _, code := runAgentDeck(t, home, "config", "set", bad, "true"); code == 0 {
			t.Fatalf("set %s accepted", bad)
		}
	}
}
