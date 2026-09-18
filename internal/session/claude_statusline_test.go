package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/quota"
)

// stubUsageFeedExecutable pins the binary the feed wrapper writes.
func stubUsageFeedExecutable(t *testing.T, path string) {
	t.Helper()
	prev := hookExecutablePath
	hookExecutablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { hookExecutablePath = prev })
}

func statusLineCommand(t *testing.T, path string) (string, map[string]json.RawMessage) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("settings.json is not valid JSON after the install: %v\n%s", err, data)
	}
	var sl map[string]json.RawMessage
	if err := json.Unmarshal(root["statusLine"], &sl); err != nil {
		t.Fatalf("statusLine: %v\n%s", err, data)
	}
	var cmd string
	_ = json.Unmarshal(sl["command"], &cmd)
	return cmd, sl
}

// TestInstallUsageFeed_WrapsExistingStatusLine is the maintainer's Mac
// shape: a statusLine script, wrapped once, everything else preserved,
// never double-wrapped.
func TestInstallUsageFeed_WrapsExistingStatusLine(t *testing.T) {
	stubUsageFeedExecutable(t, "/opt/homebrew/bin/agent-deck")
	dir := t.TempDir()
	path := writeSettings(t, dir, `{
  "model": "opus",
  "statusLine": {
    "type": "command",
    "command": "~/.claude/statusline.sh",
    "padding": 0
  },
  "hooks": {}
}
`)
	changed, err := InstallUsageFeed(dir, "personal")
	if err != nil || !changed {
		t.Fatalf("InstallUsageFeed = %v, %v; want changed", changed, err)
	}
	cmd, sl := statusLineCommand(t, path)
	want := "/opt/homebrew/bin/agent-deck -p personal usage ingest claude -- ~/.claude/statusline.sh"
	if cmd != want {
		t.Fatalf("command = %q, want %q", cmd, want)
	}
	if string(sl["padding"]) != "0" || string(sl["type"]) != `"command"` {
		t.Fatalf("statusLine siblings not preserved: %v", sl)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"model": "opus"`) || !strings.Contains(string(data), `"hooks": {}`) {
		t.Fatalf("other keys not preserved:\n%s", data)
	}
	// Order preserved: model stays first.
	if strings.Index(string(data), `"model"`) > strings.Index(string(data), `"statusLine"`) {
		t.Fatalf("key order changed:\n%s", data)
	}

	before, _ := os.ReadFile(path)
	changed, err = InstallUsageFeed(dir, "personal")
	if err != nil || changed {
		t.Fatalf("second install = %v, %v; want no change", changed, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("second install rewrote the file:\n%s", after)
	}
	if cmd, _ := statusLineCommand(t, path); cmd != want {
		t.Fatalf("double-wrapped: %q", cmd)
	}
}

// TestInstallUsageFeed_NoStatusLine installs the plain ingester when the slot
// has no statusLine at all (agentbox's bob-team-a).
func TestInstallUsageFeed_NoStatusLine(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	dir := t.TempDir()
	path := writeSettings(t, dir, `{"permissions": {"allow": ["Bash"]}}`)
	changed, err := InstallUsageFeed(dir, "bob-team-a")
	if err != nil || !changed {
		t.Fatalf("InstallUsageFeed = %v, %v", changed, err)
	}
	cmd, sl := statusLineCommand(t, path)
	if want := "/usr/local/bin/agent-deck -p bob-team-a usage ingest claude"; cmd != want {
		t.Fatalf("command = %q, want %q", cmd, want)
	}
	if string(sl["type"]) != `"command"` {
		t.Fatalf("type = %s", sl["type"])
	}
}

// TestInstallUsageFeed_MissingSettings creates settings.json for a slot whose
// config dir has none yet.
func TestInstallUsageFeed_MissingSettings(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	dir := filepath.Join(t.TempDir(), ".claude-new")
	changed, err := InstallUsageFeed(dir, "new")
	if err != nil || !changed {
		t.Fatalf("InstallUsageFeed = %v, %v", changed, err)
	}
	if cmd, _ := statusLineCommand(t, filepath.Join(dir, "settings.json")); cmd != "/usr/local/bin/agent-deck -p new usage ingest claude" {
		t.Fatalf("command = %q", cmd)
	}
}

// TestInstallUsageFeed_ShellCommand keeps a command with shell syntax
// byte-for-byte by running it through sh -c, so pipes, && and env prefixes
// still mean what they meant.
func TestInstallUsageFeed_ShellCommand(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	dir := t.TempDir()
	path := writeSettings(t, dir, `{"statusLine":{"type":"command","command":"FOO=1 bun run ~/sl.ts | head -1"}}`)
	if _, err := InstallUsageFeed(dir, "work"); err != nil {
		t.Fatal(err)
	}
	cmd, _ := statusLineCommand(t, path)
	want := "/usr/local/bin/agent-deck -p work usage ingest claude -- sh -c 'FOO=1 bun run ~/sl.ts | head -1'"
	if cmd != want {
		t.Fatalf("command = %q, want %q", cmd, want)
	}
	feed := UsageFeedStatus(dir, "work")
	if !feed.Wired || feed.Inner != "FOO=1 bun run ~/sl.ts | head -1" {
		t.Fatalf("status = %+v", feed)
	}
}

// TestInstallUsageFeed_RepinsOtherBinary rewrites a wrapper that names
// another agent-deck binary (or slot) to this one, keeping the wrapped
// command verbatim.
func TestInstallUsageFeed_RepinsOtherBinary(t *testing.T) {
	stubUsageFeedExecutable(t, "/opt/homebrew/bin/agent-deck")
	dir := t.TempDir()
	path := writeSettings(t, dir, `{"statusLine":{"type":"command","command":"/usr/local/bin/agent-deck -p personal usage ingest claude -- ~/.claude/statusline.sh --fancy"}}`)
	changed, err := InstallUsageFeed(dir, "personal")
	if err != nil || !changed {
		t.Fatalf("InstallUsageFeed = %v, %v", changed, err)
	}
	cmd, _ := statusLineCommand(t, path)
	if want := "/opt/homebrew/bin/agent-deck -p personal usage ingest claude -- ~/.claude/statusline.sh --fancy"; cmd != want {
		t.Fatalf("command = %q, want %q", cmd, want)
	}
}

// TestInstallUsageFeed_UnpinnableKeepsProgram: a dev build writes the bare
// program (or keeps the one already pinned), never its own path.
func TestInstallUsageFeed_UnpinnableKeepsProgram(t *testing.T) {
	stubUsageFeedExecutable(t, "")
	dir := t.TempDir()
	path := writeSettings(t, dir, `{"statusLine":{"type":"command","command":"~/.claude/statusline.sh"}}`)
	if _, err := InstallUsageFeed(dir, "personal"); err != nil {
		t.Fatal(err)
	}
	cmd, _ := statusLineCommand(t, path)
	if want := "agent-deck -p personal usage ingest claude -- ~/.claude/statusline.sh"; cmd != want {
		t.Fatalf("command = %q, want %q", cmd, want)
	}
}

// TestInstallUsageFeed_MalformedSettings never writes over a file it cannot
// parse.
func TestInstallUsageFeed_MalformedSettings(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	dir := t.TempDir()
	path := writeSettings(t, dir, `{"statusLine": `)
	if _, err := InstallUsageFeed(dir, "personal"); err == nil {
		t.Fatal("expected a parse error")
	}
	data, _ := os.ReadFile(path)
	if string(data) != `{"statusLine": ` {
		t.Fatalf("file was rewritten: %s", data)
	}
}

// TestRemoveUsageFeed restores the wrapped command (or drops the plain
// ingester entry) on uninstall.
func TestRemoveUsageFeed(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	dir := t.TempDir()
	path := writeSettings(t, dir, `{"statusLine":{"type":"command","command":"~/.claude/statusline.sh","padding":0}}`)
	if _, err := InstallUsageFeed(dir, "personal"); err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveUsageFeed(dir)
	if err != nil || !removed {
		t.Fatalf("RemoveUsageFeed = %v, %v", removed, err)
	}
	cmd, sl := statusLineCommand(t, path)
	if cmd != "~/.claude/statusline.sh" || string(sl["padding"]) != "0" {
		t.Fatalf("restore: cmd=%q sl=%v", cmd, sl)
	}

	plain := t.TempDir()
	plainPath := writeSettings(t, plain, `{"a":1}`)
	if _, err := InstallUsageFeed(plain, "personal"); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveUsageFeed(plain); err != nil || !removed {
		t.Fatalf("RemoveUsageFeed(plain) = %v, %v", removed, err)
	}
	data, _ := os.ReadFile(plainPath)
	if strings.Contains(string(data), "statusLine") {
		t.Fatalf("plain ingester entry not dropped:\n%s", data)
	}
}

// TestUsageFeedStatus pins the three wiring states hooks status reports.
func TestUsageFeedStatus(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	t.Run("not wired, script", func(t *testing.T) {
		dir := t.TempDir()
		writeSettings(t, dir, `{"statusLine":{"type":"command","command":"~/.claude/statusline.sh"}}`)
		feed := UsageFeedStatus(dir, "personal")
		if feed.Wired || feed.Command != "~/.claude/statusline.sh" {
			t.Fatalf("status = %+v", feed)
		}
	})
	t.Run("not wired, none", func(t *testing.T) {
		dir := t.TempDir()
		writeSettings(t, dir, `{}`)
		feed := UsageFeedStatus(dir, "personal")
		if feed.Wired || feed.Command != "" {
			t.Fatalf("status = %+v", feed)
		}
	})
	t.Run("wired", func(t *testing.T) {
		dir := t.TempDir()
		writeSettings(t, dir, `{"statusLine":{"type":"command","command":"/usr/local/bin/agent-deck -p personal usage ingest claude -- ~/.claude/statusline.sh"}}`)
		feed := UsageFeedStatus(dir, "personal")
		if !feed.Wired || feed.Inner != "~/.claude/statusline.sh" || feed.FeedSlot != "personal" {
			t.Fatalf("status = %+v", feed)
		}
	})
	t.Run("wired for another slot counts as not wired for this one", func(t *testing.T) {
		dir := t.TempDir()
		writeSettings(t, dir, `{"statusLine":{"type":"command","command":"agent-deck -p work usage ingest claude"}}`)
		feed := UsageFeedStatus(dir, "personal")
		if feed.Wired || feed.FeedSlot != "work" {
			t.Fatalf("status = %+v", feed)
		}
	})
}

// TestCollectAccountUsage_Reasons pins the reason an unknown slot carries:
// no feed when the statusLine is not wired, no data when wired but never
// written, unreadable when the file exists but cannot be used.
func TestCollectAccountUsage_Reasons(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	newAccountUsageTestHome(t)
	root := t.TempDir()
	mk := func(name, settings string) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeSettings(t, dir, settings)
		return dir
	}
	wired := `{"statusLine":{"type":"command","command":"/usr/local/bin/agent-deck -p %s usage ingest claude"}}`
	config := &UserConfig{Profiles: map[string]ProfileSettings{
		"nofeed":     {Claude: ProfileClaudeSettings{ConfigDir: mk("nofeed", `{}`)}},
		"nodata":     {Claude: ProfileClaudeSettings{ConfigDir: mk("nodata", strings.ReplaceAll(wired, "%s", "nodata"))}},
		"unreadable": {Claude: ProfileClaudeSettings{ConfigDir: mk("unreadable", strings.ReplaceAll(wired, "%s", "unreadable"))}},
		"known":      {Claude: ProfileClaudeSettings{ConfigDir: mk("known", `{}`)}},
	}}
	store, err := quota.NewStore("unreadable")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(), quota.ProviderClaude+".json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	saveClaudeSnapshot(t, "known", quota.Snapshot{
		Windows:   []quota.Window{{Kind: quota.WindowFiveHour, Label: "5h", UsedPercentage: 8}},
		UpdatedAt: now.Unix(),
	})

	got := CollectAccountUsage(config, NewAccountUsageCache(), now)
	byName := map[string]AccountUsage{}
	for _, u := range got {
		byName[u.Name] = u
	}
	if u := byName["nofeed"]; u.Known || u.UnknownReason != AccountUsageNoFeed {
		t.Errorf("nofeed = %+v", u)
	}
	if u := byName["nodata"]; u.Known || u.UnknownReason != AccountUsageNoData {
		t.Errorf("nodata = %+v", u)
	}
	if u := byName["unreadable"]; u.Known || u.UnknownReason != AccountUsageUnreadable {
		t.Errorf("unreadable = %+v", u)
	}
	// A slot with data is known regardless of how the file got there.
	if u := byName["known"]; !u.Known || u.UnknownReason != "" {
		t.Errorf("known = %+v", u)
	}
}

// TestInstallUsageFeeds wires every configured slot and reports each.
func TestInstallUsageFeeds(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSettings(t, a, `{"statusLine":{"type":"command","command":"~/sl.sh"}}`)
	writeSettings(t, b, `{}`)
	config := &UserConfig{Profiles: map[string]ProfileSettings{
		"a": {Claude: ProfileClaudeSettings{ConfigDir: a}},
		"b": {Claude: ProfileClaudeSettings{ConfigDir: b}},
		"c": {},
	}}
	results := InstallUsageFeeds(config)
	if len(results) != 2 || results[0].Slot != "a" || results[1].Slot != "b" {
		t.Fatalf("results = %+v", results)
	}
	for _, r := range results {
		if r.Err != nil || !r.Changed || !r.Feed.Wired {
			t.Errorf("slot %s: %+v", r.Slot, r)
		}
	}
	if results[0].Feed.Inner != "~/sl.sh" || results[1].Feed.Inner != "" {
		t.Errorf("inner commands: %+v", results)
	}
}
