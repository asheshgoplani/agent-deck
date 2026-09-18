package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// setupUsageFeedTest sandboxes HOME/XDG and configures two Claude account
// slots: "personal" with the maintainer's statusLine script and "work" with
// no statusLine at all.
func setupUsageFeedTest(t *testing.T) (home, personalDir, workDir string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	personalDir = filepath.Join(home, ".claude")
	workDir = filepath.Join(home, ".claude-work")
	for _, d := range []string{personalDir, workDir, filepath.Join(home, ".config", "agent-deck")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(personalDir, "settings.json"), []byte(`{
  "statusLine": {
    "type": "command",
    "command": "~/.claude/statusline.sh",
    "padding": 0
  }
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "settings.json"), []byte(`{"model": "opus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-deck", "config.toml"), []byte(`
[profiles.personal.claude]
config_dir = "~/.claude"
[profiles.work.claude]
config_dir = "~/.claude-work"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return home, personalDir, workDir
}

// TestHooksInstall_WiresUsageFeedForEverySlot: `hooks install` wraps every
// configured slot's statusLine (or installs the plain ingester) and says so.
func TestHooksInstall_WiresUsageFeedForEverySlot(t *testing.T) {
	_, personalDir, workDir := setupUsageFeedTest(t)

	out := captureStdout(t, handleHooksInstall)

	personal := session.UsageFeedStatus(personalDir, "personal")
	if !personal.Wired || personal.Inner != "~/.claude/statusline.sh" {
		t.Errorf("personal not wired: %+v", personal)
	}
	work := session.UsageFeedStatus(workDir, "work")
	if !work.Wired || work.Inner != "" {
		t.Errorf("work not wired: %+v", work)
	}
	for _, want := range []string{"Usage feed:", "personal", "work", "usage ingest claude"} {
		if !strings.Contains(out, want) {
			t.Errorf("install output missing %q:\n%s", want, out)
		}
	}
	data, _ := os.ReadFile(filepath.Join(personalDir, "settings.json"))
	if !strings.Contains(string(data), `"padding": 0`) {
		t.Errorf("statusLine padding lost:\n%s", data)
	}

	// Idempotent: a second install changes nothing.
	before, _ := os.ReadFile(filepath.Join(personalDir, "settings.json"))
	captureStdout(t, handleHooksInstall)
	after, _ := os.ReadFile(filepath.Join(personalDir, "settings.json"))
	if !bytes.Equal(before, after) {
		t.Errorf("second install rewrote settings.json:\n%s", after)
	}
}

// TestHooksUninstall_RestoresStatusLine: uninstall puts the original
// statusLine command back and drops a plain ingester entry.
func TestHooksUninstall_RestoresStatusLine(t *testing.T) {
	_, personalDir, workDir := setupUsageFeedTest(t)
	captureStdout(t, handleHooksInstall)
	captureStdout(t, handleHooksUninstall)
	if feed := session.UsageFeedStatus(personalDir, "personal"); feed.Wired || feed.Command != "~/.claude/statusline.sh" {
		t.Errorf("personal not restored: %+v", feed)
	}
	if feed := session.UsageFeedStatus(workDir, "work"); feed.Wired || feed.Command != "" {
		t.Errorf("work ingester not removed: %+v", feed)
	}
}

// TestPrintUsageFeedStatus pins the per-slot line `hooks status` prints:
// wired / not wired and the cache age.
func TestPrintUsageFeedStatus(t *testing.T) {
	now := time.Now()
	feeds := []session.UsageFeed{
		{Slot: "personal", ConfigDir: "/h/.claude", Wired: true, Command: "/usr/local/bin/agent-deck -p personal usage ingest claude -- ~/.claude/statusline.sh", Inner: "~/.claude/statusline.sh"},
		{Slot: "work", ConfigDir: "/h/.claude-work", Command: "~/.claude/statusline.sh"},
		{Slot: "buddii", ConfigDir: "/h/.claude-buddii"},
	}
	ages := map[string]usageCacheAge{
		"personal": {Exists: true, Age: 3 * time.Minute},
	}
	var out bytes.Buffer
	printUsageFeedStatus(&out, feeds, ages, now)
	text := out.String()
	for _, want := range []string{
		"Usage feed:",
		"personal  wired (wraps ~/.claude/statusline.sh) · cache 3m old",
		"work      not wired (statusLine: ~/.claude/statusline.sh) · no cache",
		"buddii    not wired (no statusLine) · no cache",
		"Run 'agent-deck hooks install' to wire the usage feed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status output missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	printUsageFeedStatus(&out, feeds[:1], ages, now)
	if strings.Contains(out.String(), "Run 'agent-deck hooks install'") {
		t.Errorf("all wired must not ask for a reinstall:\n%s", out.String())
	}
	out.Reset()
	printUsageFeedStatus(&out, nil, nil, now)
	if !strings.Contains(out.String(), "Usage feed: no Claude account slots configured") {
		t.Errorf("no slots:\n%s", out.String())
	}
}

// TestHooksStatus_ReportsUsageFeed runs the real status against the sandbox.
func TestHooksStatus_ReportsUsageFeed(t *testing.T) {
	setupUsageFeedTest(t)
	out := captureStdout(t, handleHooksStatus)
	for _, want := range []string{"Usage feed:", "personal  not wired (statusLine: ~/.claude/statusline.sh)", "work      not wired (no statusLine)"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
	captureStdout(t, handleHooksInstall)
	out = captureStdout(t, handleHooksStatus)
	if !strings.Contains(out, "personal  wired (wraps ~/.claude/statusline.sh) · no cache") || !strings.Contains(out, "work      wired · no cache") {
		t.Errorf("status after install:\n%s", out)
	}
}
