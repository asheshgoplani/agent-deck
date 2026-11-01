package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
)

// Issue #3011: installing the statusLine wrapper re-wrapped a command that
// was already a statusline-wrap invocation, one level per heal (TUI start,
// notify daemon start, hooks install), until settings.json was MBs of
// nested shell quoting and Claude Code could no longer load it.

// wrapStatusLine builds one wrapper level the way every agent-deck release
// that wrote the sh -c form did.
func wrapStatusLine(program, slot, inner string) string {
	return program + " -p " + slot + " usage statusline-wrap -- sh -c " + shellescape.Quote(inner)
}

func readSettingsText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func settingsBackups(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, usageFeedSettingsBackupPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// The reporter's binary is agent-deck-bin (an install that puts a launcher
// script named agent-deck in front of the real binary). The wrapper the
// install wrote named that binary, the next install did not recognise its
// own wrapper and wrapped it again.
func TestIssue3011_InstallTwiceIsOneWrap(t *testing.T) {
	for _, exe := range []string{
		"/home/u/.local/bin/agent-deck-bin",
		"/opt/homebrew/bin/agent-deck",
		`C:\Users\u\bin\agent-deck.exe`,
	} {
		t.Run(filepath.Base(exe), func(t *testing.T) {
			stubUsageFeedExecutable(t, exe)
			dir := t.TempDir()
			path := writeSettings(t, dir, `{"model":"opus","statusLine":{"type":"command","command":"~/.claude/statusline.sh"}}`)
			if changed, err := InstallUsageFeed(dir, "default"); err != nil || !changed {
				t.Fatalf("first install = %v, %v; want changed", changed, err)
			}
			first := readSettingsText(t, path)
			for i := 0; i < 3; i++ {
				if changed, err := InstallUsageFeed(dir, "default"); err != nil || changed {
					t.Fatalf("install #%d = %v, %v; want no change", i+2, changed, err)
				}
			}
			if got := readSettingsText(t, path); got != first {
				t.Fatalf("a repeat install rewrote settings.json:\n%s", got)
			}
			cmd, _ := statusLineCommand(t, path)
			want := wrapStatusLine(shellescape.Quote(exe), "default", "~/.claude/statusline.sh")
			if cmd != want {
				t.Fatalf("command = %q, want %q", cmd, want)
			}
			if n := strings.Count(cmd, "statusline-wrap"); n != 1 {
				t.Fatalf("statusline-wrap appears %d times: %q", n, cmd)
			}
		})
	}
}

// A settings.json already damaged by the bug (three levels, mixed binaries
// and profiles, the oldest level in the legacy direct `--` form) collapses
// to one wrap around the user's own command, with a backup of the damaged
// file taken first and every other key left alone.
func TestIssue3011_NestedWrapCollapsesToOne(t *testing.T) {
	stubUsageFeedExecutable(t, "/home/u/.local/bin/agent-deck-bin")
	user := "~/.claude/statusline.sh"
	level1 := "/usr/local/bin/agent-deck -p work usage statusline-wrap -- " + user
	level2 := wrapStatusLine("/home/u/.local/bin/agent-deck-bin", "default", level1)
	level3 := wrapStatusLine("'/home/u/.local/bin/agent-deck-bin'", "'default'", level2)
	dir := t.TempDir()
	original := `{"model":"opus","statusLine":{"type":"command","command":` + string(mustMarshal(level3)) + `,"padding":2},"hooks":{}}`
	path := writeSettings(t, dir, original)

	if changed, err := InstallUsageFeed(dir, "default"); err != nil || !changed {
		t.Fatalf("install = %v, %v; want the nested command repaired", changed, err)
	}
	cmd, sl := statusLineCommand(t, path)
	if want := wrapStatusLine("/home/u/.local/bin/agent-deck-bin", "default", user); cmd != want {
		t.Fatalf("command = %q, want %q", cmd, want)
	}
	if string(sl["padding"]) != "2" {
		t.Fatalf("statusLine siblings lost: %v", sl)
	}
	data := readSettingsText(t, path)
	if !strings.Contains(data, `"model": "opus"`) || !strings.Contains(data, `"hooks": {}`) {
		t.Fatalf("other keys changed:\n%s", data)
	}
	backups := settingsBackups(t, dir)
	if len(backups) != 1 || readSettingsText(t, backups[0]) != original {
		t.Fatalf("want one backup holding the damaged file, got %v", backups)
	}

	// Uninstall restores the user's own command, not a wrapper level.
	if removed, err := RemoveUsageFeed(dir); err != nil || !removed {
		t.Fatalf("RemoveUsageFeed = %v, %v", removed, err)
	}
	if cmd, _ := statusLineCommand(t, path); cmd != user {
		t.Fatalf("after uninstall command = %q, want %q", cmd, user)
	}
}

// The existing wrap names another binary (a different install path, an
// agent-deck-bin) and another profile: it is rewritten to this binary and
// slot around the same inner command, never wrapped.
func TestIssue3011_OtherBinaryAndProfileRewrapped(t *testing.T) {
	stubUsageFeedExecutable(t, "/opt/homebrew/bin/agent-deck")
	for _, existing := range []string{
		wrapStatusLine("/home/u/.local/bin/agent-deck-bin", "work", "bun ~/sl.ts"),
		wrapStatusLine("/usr/local/bin/agent-deck", "'work'", "bun ~/sl.ts"),
		"AGENTDECK_X=1 /usr/bin/agent-deck --profile=work usage statusline-wrap -- sh -c 'bun ~/sl.ts'",
		"/home/u/.local/bin/agent-deck-bin -p=work usage ingest claude -- bun ~/sl.ts",
	} {
		dir := t.TempDir()
		path := writeSettings(t, dir, `{"statusLine":{"type":"command","command":`+string(mustMarshal(existing))+`}}`)
		if _, err := InstallUsageFeed(dir, "personal"); err != nil {
			t.Fatal(err)
		}
		cmd, _ := statusLineCommand(t, path)
		if want := wrapStatusLine("/opt/homebrew/bin/agent-deck", "personal", "bun ~/sl.ts"); cmd != want {
			t.Errorf("existing %q:\n got  %q\n want %q", existing, cmd, want)
		}
	}
}

// A user command with quotes and spaces survives any number of installs and
// an uninstall byte for byte.
func TestIssue3011_UserCommandWithQuotesAndSpaces(t *testing.T) {
	user := `"/Users/me/My Scripts/status line.sh" --label 'it'"'"'s' | head -c 80`
	dir := t.TempDir()
	path := writeSettings(t, dir, `{"statusLine":{"type":"command","command":`+string(mustMarshal(user))+`}}`)
	for _, exe := range []string{"/home/u/.local/bin/agent-deck-bin", "/opt/homebrew/bin/agent-deck", "/home/u/.local/bin/agent-deck-bin"} {
		stubUsageFeedExecutable(t, exe)
		if _, err := InstallUsageFeed(dir, "default"); err != nil {
			t.Fatal(err)
		}
		cmd, _ := statusLineCommand(t, path)
		if want := wrapStatusLine(shellescape.Quote(exe), "default", user); cmd != want {
			t.Fatalf("exe %s: command = %q, want %q", exe, cmd, want)
		}
		if feed := UsageFeedStatus(dir, "default"); feed.Inner != user || !feed.Wired {
			t.Fatalf("exe %s: status = %+v", exe, feed)
		}
	}
	if _, err := RemoveUsageFeed(dir); err != nil {
		t.Fatal(err)
	}
	if cmd, _ := statusLineCommand(t, path); cmd != user {
		t.Fatalf("after uninstall command = %q, want %q", cmd, user)
	}
}

// An already-correct single wrap is a no-op: same bytes, same mtime, no
// statusline backup, no settings backup. That includes the healthy value
// from the report, the legacy direct `--` form this binary would not write
// itself.
func TestIssue3011_CorrectWrapWritesNothing(t *testing.T) {
	exe := "/home/u/.local/bin/agent-deck-bin"
	stubUsageFeedExecutable(t, exe)
	for _, command := range []string{
		wrapStatusLine(exe, "default", "~/.claude/statusline.sh"),
		exe + " -p default usage statusline-wrap -- ~/.claude/statusline.sh",
		exe + " -p default usage statusline-wrap",
	} {
		dir := t.TempDir()
		content := "{\n  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": " + string(mustMarshal(command)) + "\n  }\n}\n"
		path := writeSettings(t, dir, content)
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		changed, err := InstallUsageFeed(dir, "default")
		if err != nil || changed {
			t.Errorf("%q: install = %v, %v; want no change", command, changed, err)
		}
		after, _ := os.Stat(path)
		if got := readSettingsText(t, path); got != content || !after.ModTime().Equal(before.ModTime()) {
			t.Errorf("%q: settings.json was rewritten:\n%s", command, got)
		}
		if _, err := os.Stat(usageFeedBackupPath(dir)); !os.IsNotExist(err) {
			t.Errorf("%q: a no-op install wrote the statusline backup", command)
		}
		if b := settingsBackups(t, dir); len(b) != 0 {
			t.Errorf("%q: a no-op install wrote %v", command, b)
		}
	}
}

// The repair backup must not stand in for the hooks heal's one-time backup
// (backupSettingsOnce skips when a healBackupPrefix file exists).
func TestIssue3011_RepairBackupDoesNotSuppressHealBackup(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	nested := wrapStatusLine("/usr/local/bin/agent-deck", "default", wrapStatusLine("/usr/local/bin/agent-deck", "default", "x"))
	dir := t.TempDir()
	writeSettings(t, dir, `{"statusLine":{"type":"command","command":`+string(mustMarshal(nested))+`}}`)
	if _, err := InstallUsageFeed(dir, "default"); err != nil {
		t.Fatal(err)
	}
	if len(settingsBackups(t, dir)) != 1 {
		t.Fatal("want the repair backup")
	}
	if err := backupSettingsOnce(dir); err != nil {
		t.Fatal(err)
	}
	if heal, _ := filepath.Glob(filepath.Join(dir, healBackupPrefix+"*")); len(heal) != 1 {
		t.Fatalf("heal backup = %v; the repair backup suppressed it", heal)
	}
}

// A statusline backup saved by an affected release can hold a wrapper as
// the "original"; uninstall restores the user's command it wraps, not the
// wrapper.
func TestIssue3011_UninstallUnwrapsStaleBackup(t *testing.T) {
	exe := "/home/u/.local/bin/agent-deck-bin"
	stubUsageFeedExecutable(t, exe)
	current := wrapStatusLine(exe, "default", "~/.claude/statusline.sh")
	stale := wrapStatusLine(exe, "default", current)
	dir := t.TempDir()
	path := writeSettings(t, dir, `{"statusLine":{"type":"command","command":`+string(mustMarshal(current))+`}}`)
	backup := `{"installed_command":` + string(mustMarshal(current)) + `,"original_statusline":{"type":"command","command":` + string(mustMarshal(stale)) + `}}`
	if err := os.WriteFile(usageFeedBackupPath(dir), []byte(backup), 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveUsageFeed(dir); err != nil || !removed {
		t.Fatalf("RemoveUsageFeed = %v, %v", removed, err)
	}
	if cmd, _ := statusLineCommand(t, path); cmd != "~/.claude/statusline.sh" {
		t.Fatalf("after uninstall command = %q, want the user's script", cmd)
	}
}
