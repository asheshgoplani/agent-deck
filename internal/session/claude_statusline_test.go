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
	want := "/opt/homebrew/bin/agent-deck -p personal usage statusline-wrap -- sh -c '~/.claude/statusline.sh'"
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
	if want := "/usr/local/bin/agent-deck -p bob-team-a usage statusline-wrap"; cmd != want {
		t.Fatalf("command = %q, want %q", cmd, want)
	}
	if string(sl["type"]) != `"command"` {
		t.Fatalf("type = %s", sl["type"])
	}
}

// TestInstallUsageFeed_MissingSettings creates settings.json for a slot whose
// config dir exists but holds none yet.
func TestInstallUsageFeed_MissingSettings(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	dir := t.TempDir()
	changed, err := InstallUsageFeed(dir, "new")
	if err != nil || !changed {
		t.Fatalf("InstallUsageFeed = %v, %v", changed, err)
	}
	if cmd, _ := statusLineCommand(t, filepath.Join(dir, "settings.json")); cmd != "/usr/local/bin/agent-deck -p new usage statusline-wrap" {
		t.Fatalf("command = %q", cmd)
	}
}

// TestInstallUsageFeed_MissingDir (review finding 5): a slot whose config
// dir does not exist is skipped, never created; status names the reason.
func TestInstallUsageFeed_MissingDir(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	dir := filepath.Join(t.TempDir(), ".claude-new")
	changed, err := InstallUsageFeed(dir, "new")
	if err != nil || changed {
		t.Fatalf("InstallUsageFeed = %v, %v; want skipped", changed, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("install created the config dir: %v", err)
	}
	if feed := UsageFeedStatus(dir, "new"); feed.Wired || feed.Blocked != usageFeedBlockedSlotDir {
		t.Fatalf("status = %+v", feed)
	}
}

// TestInstallUsageFeed_RefusesUnusableSlotName (review finding 1): a profile
// name the quota cache cannot store ("team.a": profile names allow a dot,
// the cache directory does not) is never wired, since the ingester would
// have nowhere to write; the file stays as it was and status says why.
func TestInstallUsageFeed_RefusesUnusableSlotName(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	dir := t.TempDir()
	original := `{"statusLine":{"type":"command","command":"~/sl.sh"}}`
	path := writeSettings(t, dir, original)
	changed, err := InstallUsageFeed(dir, "team.a")
	if err != nil || changed {
		t.Fatalf("InstallUsageFeed = %v, %v; want skipped", changed, err)
	}
	if data, _ := os.ReadFile(path); string(data) != original {
		t.Fatalf("file was rewritten:\n%s", data)
	}
	feed := UsageFeedStatus(dir, "team.a")
	if feed.Wired || !strings.Contains(feed.Blocked, `unusable profile name "team.a"`) {
		t.Fatalf("status = %+v", feed)
	}
	if feed := UsageFeedStatus(dir, "team-a"); feed.Blocked != "" {
		t.Fatalf("a usable name must not be blocked: %+v", feed)
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
	want := "/usr/local/bin/agent-deck -p work usage statusline-wrap -- sh -c 'FOO=1 bun run ~/sl.ts | head -1'"
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
	if want := "/opt/homebrew/bin/agent-deck -p personal usage statusline-wrap -- sh -c '~/.claude/statusline.sh --fancy'"; cmd != want {
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
	if want := "agent-deck -p personal usage statusline-wrap -- sh -c '~/.claude/statusline.sh'"; cmd != want {
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

	// Review finding 7: an object install only added a command to comes
	// back as it was; only what install itself writes (the command and a
	// "type": "command") goes. The backup distinguishes pre-existing
	// type-only entries from the type added by installation.
	for _, c := range []struct{ before, after string }{

		{`{"statusLine":{"padding":0},"model":"opus"}`, `{"statusLine":{"padding":0},"model":"opus"}`},
		{`{"statusLine":{"type":"command"},"model":"opus"}`, `{"statusLine":{"type":"command"},"model":"opus"}`},
		{`{"statusLine":{"type":"command","padding":0},"model":"opus"}`, `{"statusLine":{"type":"command","padding":0},"model":"opus"}`},
	} {
		dir := t.TempDir()
		path := writeSettings(t, dir, c.before)
		if _, err := InstallUsageFeed(dir, "personal"); err != nil {
			t.Fatal(err)
		}
		if removed, err := RemoveUsageFeed(dir); err != nil || !removed {
			t.Fatalf("%s: RemoveUsageFeed = %v, %v", c.before, removed, err)
		}
		data, _ := os.ReadFile(path)
		var got, want any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s: %v\n%s", c.before, err, data)
		}
		_ = json.Unmarshal([]byte(c.after), &want)
		if gotJSON, wantJSON := mustMarshal(got), mustMarshal(want); string(gotJSON) != string(wantJSON) {
			t.Errorf("%s: after uninstall %s, want %s", c.before, gotJSON, wantJSON)
		}
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
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "absent"))
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

func TestUsageFeedDiscoveryDefaultAndAliases(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := &UserConfig{Profiles: map[string]ProfileSettings{}}
	slots := ClaudeUsageFeedSlots(cfg)
	if len(slots) != 1 || slots[0].Name != "default" || slots[0].ConfigDir != dir {
		t.Fatalf("default discovery: %+v", slots)
	}
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	cfg.Profiles["work"] = ProfileSettings{Claude: ProfileClaudeSettings{ConfigDir: alias}}
	slots = ClaudeUsageFeedSlots(cfg)
	if len(slots) != 1 || slots[0].Name != "work" {
		t.Fatalf("alias must own default: %+v", slots)
	}
	cfg.Profiles["default"] = ProfileSettings{Claude: ProfileClaudeSettings{ConfigDir: filepath.Join(home, "elsewhere")}}
	delete(cfg.Profiles, "work")
	slots = ClaudeUsageFeedSlots(cfg)
	if len(slots) != 2 || slots[1].Name != "default-claude-1" {
		t.Fatalf("collision: %+v", slots)
	}
}

func TestUsageFeedPreservesUnsupportedAndUserReplacement(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	dir := t.TempDir()
	original := `{"statusLine":{"type":"static","text":"hello"}}`
	path := writeSettings(t, dir, original)
	if changed, err := InstallUsageFeed(dir, "default"); changed || err == nil {
		t.Fatalf("unsupported changed=%v err=%v", changed, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatal("unsupported statusline was changed")
	}
	writeSettings(t, dir, `{"statusLine":{"command":"echo old"}}`)
	if _, err := InstallUsageFeed(dir, "default"); err != nil {
		t.Fatal(err)
	}
	replacement := `{"statusLine":{"command":"echo new","padding":2}}`
	writeSettings(t, dir, replacement)
	if changed, err := RemoveUsageFeed(dir); changed || err != nil {
		t.Fatalf("user replacement changed=%v err=%v", changed, err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != replacement {
		t.Fatal("user replacement was changed")
	}
}

func TestUsageFeedInterruptedRepinPreservesOriginal(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	dir := t.TempDir()
	path := writeSettings(t, dir, `{"statusLine":{"command":"echo original","padding":0}}`)
	if _, err := InstallUsageFeed(dir, "default"); err != nil {
		t.Fatal(err)
	}
	root, _, err := readSettingsObject(path)
	if err != nil {
		t.Fatal(err)
	}
	obj, _, err := statusLineObject(root)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the boundary where the backup was prepared but replacing
	// settings failed. Uninstall must still recognize the old generation.
	current := obj.getString("command")
	if err := saveUsageFeedBackup(dir, root, current, "/new/agent-deck -p default usage statusline-wrap -- sh -c 'echo original'"); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveUsageFeed(dir); err != nil {
		t.Fatal(err)
	}
	command, restored := statusLineCommand(t, path)
	if command != "echo original" {
		t.Fatalf("command = %q", command)
	}
	if _, exists := restored["type"]; exists {
		t.Fatal("originally absent type was not preserved")
	}
}

// The default Claude config is only wired unattended (daemon start-up, TUI
// repair) when agent-deck's own hooks are installed there, so `hooks
// uninstall` is never undone by the next heal. Named account slots keep their
// existing unconditional heal.
func TestHealUsageFeedsDefaultConfigNeedsHooks(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	original := `{"statusLine":{"type":"command","command":"echo mine"},"model":"opus"}`
	path := writeSettings(t, dir, original)
	cfg := &UserConfig{Profiles: map[string]ProfileSettings{}}
	for _, r := range HealUsageFeeds(cfg) {
		if r.Changed {
			t.Fatalf("heal wired the default config without agent-deck hooks: %+v", r)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != original {
		t.Fatalf("settings changed without hooks:\n%s", data)
	}
	if _, err := InjectClaudeHooks(dir); err != nil {
		t.Fatal(err)
	}
	results := HealUsageFeeds(cfg)
	if len(results) != 1 || !results[0].Changed || results[0].Slot != "default" {
		t.Fatalf("heal after hooks install: %+v", results)
	}
	if cmd, _ := statusLineCommand(t, path); cmd != "/usr/local/bin/agent-deck -p default usage statusline-wrap -- sh -c 'echo mine'" {
		t.Fatalf("command = %q", cmd)
	}
}

// [claude] statusline_feed = false is the opt-out: nothing installs or heals
// a statusLine wrapper, while uninstall still restores an earlier one.
func TestUsageFeedOptOut(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	slotDir := filepath.Join(home, "work")
	if err := os.MkdirAll(slotDir, 0700); err != nil {
		t.Fatal(err)
	}
	original := `{"statusLine":{"type":"command","command":"echo mine"}}`
	path := writeSettings(t, dir, original)
	slotPath := writeSettings(t, slotDir, original)
	if _, err := InjectClaudeHooks(dir); err != nil {
		t.Fatal(err)
	}
	withHooks, _ := os.ReadFile(path)
	off := false
	cfg := &UserConfig{
		Claude:   ClaudeSettings{StatuslineFeed: &off},
		Profiles: map[string]ProfileSettings{"work": {Claude: ProfileClaudeSettings{ConfigDir: slotDir}}},
	}
	if cfg.Claude.GetStatuslineFeed() {
		t.Fatal("statusline_feed = false not honoured")
	}
	if got := HealUsageFeeds(cfg); len(got) != 0 {
		t.Fatalf("heal with opt-out: %+v", got)
	}
	results := InstallUsageFeeds(cfg)
	if len(results) != 2 {
		t.Fatalf("install with opt-out: %+v", results)
	}
	for _, r := range results {
		if r.Changed || r.Err != nil || !strings.Contains(r.Feed.Blocked, "statusline_feed") {
			t.Fatalf("install with opt-out: %+v", r)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != string(withHooks) {
		t.Fatalf("default settings changed under opt-out:\n%s", data)
	}
	if data, _ := os.ReadFile(slotPath); string(data) != original {
		t.Fatalf("slot settings changed under opt-out:\n%s", data)
	}
	// Turning the feed off after it was installed: uninstall restores.
	on := true
	cfg.Claude.StatuslineFeed = &on
	InstallUsageFeeds(cfg)
	cfg.Claude.StatuslineFeed = &off
	for _, r := range RemoveUsageFeeds(cfg) {
		if r.Err != nil || !r.Changed {
			t.Fatalf("remove under opt-out: %+v", r)
		}
	}
	if cmd, _ := statusLineCommand(t, slotPath); cmd != "echo mine" {
		t.Fatalf("slot command after remove = %q", cmd)
	}
	if cmd, _ := statusLineCommand(t, path); cmd != "echo mine" {
		t.Fatalf("default command after remove = %q", cmd)
	}
}
