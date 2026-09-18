package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Messaging audit P1-2: ValidateTranscriptPath accepted only $HOME/.claude, but
// every agent-deck-launched Claude runs with CLAUDE_CONFIG_DIR pointing at a
// worker-scratch home (or a named account slot such as ~/.claude-work) and
// reports its transcript_path under THAT dir. The Stop-hook sentinel scan and
// the daemon rescan were therefore skipped for every worker, so a printed
// [DONE] never became a finished event and the conductor only ever saw
// "waiting". The roots are now: ~/.claude, $CLAUDE_CONFIG_DIR, every
// configured account slot, the [claude].config_dir, and the worker-scratch
// root; both sides are symlink-resolved before the containment check, and a
// path that resolves outside every root is rejected.

// transcriptRootsHome isolates HOME plus the XDG dirs so config.toml and the
// worker-scratch root land in the temp tree.
func transcriptRootsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENT_DECK_HOME", "")
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(name, filepath.Join(home, name))
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	return home
}

func writeTranscriptRootsConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, "XDG_CONFIG_HOME", "agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
}

func writeDoneTranscriptAt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	line := scanAssistantLine(t, "all done\n===AGENTDECK_DONE=== status=ok summary=slot transcript scanned")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTranscriptPath_AcceptsClaudeConfigDir(t *testing.T) {
	home := transcriptRootsHome(t)
	cfgDir := filepath.Join(home, "elsewhere", "claude-cfg")
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	path := filepath.Join(cfgDir, "projects", "-tmp-p", "t.jsonl")
	writeDoneTranscriptAt(t, path)

	cleaned, ok := ValidateTranscriptPath(path)
	if !ok || cleaned != path {
		t.Fatalf("transcript under $CLAUDE_CONFIG_DIR must be accepted: ok=%v cleaned=%q", ok, cleaned)
	}
	sig, found, pending := ScanTranscriptTailForDone(cleaned)
	if !found || pending || sig.Status != "ok" {
		t.Fatalf("sentinel not detected under CLAUDE_CONFIG_DIR: found=%v pending=%v sig=%+v", found, pending, sig)
	}
	// A sibling that merely shares the prefix is still outside.
	if _, ok := ValidateTranscriptPath(cfgDir + "-spoof/t.jsonl"); ok {
		t.Fatal("sibling of CLAUDE_CONFIG_DIR must be rejected")
	}
}

func TestValidateTranscriptPath_AcceptsConfiguredAccountSlots(t *testing.T) {
	home := transcriptRootsHome(t)
	writeTranscriptRootsConfig(t, home, "[profiles.work.claude]\nconfig_dir = '~/.claude-work'\n[claude]\nconfig_dir = '~/.claude-global'\n")

	// The daemon (no CLAUDE_CONFIG_DIR of its own) rescans a path a hook
	// recorded for a session running under the work slot.
	for _, dir := range []string{".claude-work", ".claude-global"} {
		path := filepath.Join(home, dir, "projects", "-tmp-p", "t.jsonl")
		writeDoneTranscriptAt(t, path)
		if cleaned, ok := ValidateTranscriptPath(path); !ok || cleaned != path {
			t.Fatalf("transcript under configured slot %s must be accepted: ok=%v cleaned=%q", dir, ok, cleaned)
		}
	}
	if _, ok := ValidateTranscriptPath(filepath.Join(home, ".claude-other", "projects", "t.jsonl")); ok {
		t.Fatal("an unconfigured sibling slot must be rejected")
	}
}

// TestValidateTranscriptPath_ConductorScratchLayout reproduces the live layout
// on the maintainer's machine: CLAUDE_CONFIG_DIR is
// ~/.agent-deck/worker-scratch/<id>/generation-N, whose `projects` entry is a
// symlink to ~/.claude/projects. Claude reports the transcript through the
// scratch path. The hook handler (with the env var) and the daemon (without)
// must both accept it, and the scan must find the sentinel.
func TestValidateTranscriptPath_ConductorScratchLayout(t *testing.T) {
	home := transcriptRootsHome(t)
	realProjects := filepath.Join(home, ".claude", "projects")
	if err := os.MkdirAll(realProjects, 0o700); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(workerScratchDirRoot(), "switch-abc123", "generation-42")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realProjects, filepath.Join(scratch, "projects")); err != nil {
		t.Fatal(err)
	}
	reported := filepath.Join(scratch, "projects", "-Users-me-conductor", "c703794b.jsonl")
	writeDoneTranscriptAt(t, filepath.Join(realProjects, "-Users-me-conductor", "c703794b.jsonl"))

	t.Run("hook_handler_with_env", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", scratch)
		cleaned, ok := ValidateTranscriptPath(reported)
		if !ok {
			t.Fatalf("scratch-layout transcript must be accepted with CLAUDE_CONFIG_DIR set")
		}
		if sig, found, _ := ScanTranscriptTailForDone(cleaned); !found || sig.Summary != "slot transcript scanned" {
			t.Fatalf("sentinel not detected: found=%v sig=%+v", found, sig)
		}
	})
	t.Run("daemon_without_env", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		cleaned, ok := ValidateTranscriptPath(reported)
		if !ok {
			t.Fatalf("scratch-layout transcript must be accepted by the daemon rescan (symlink resolves under ~/.claude)")
		}
		if _, found, _ := ScanTranscriptTailForDone(cleaned); !found {
			t.Fatal("sentinel not detected on daemon rescan")
		}
	})
}

func TestValidateTranscriptPath_SymlinkEscapeFailsClosed(t *testing.T) {
	home := transcriptRootsHome(t)
	outside := filepath.Join(home, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A directory under ~/.claude that links OUT of every root.
	if err := os.Symlink(outside, filepath.Join(home, ".claude", "projects")); err != nil {
		t.Fatal(err)
	}
	escaped := filepath.Join(home, ".claude", "projects", "t.jsonl")
	writeDoneTranscriptAt(t, filepath.Join(outside, "t.jsonl"))
	if _, ok := ValidateTranscriptPath(escaped); ok {
		t.Fatal("a path that resolves outside every root must be rejected")
	}
	// A path that does not exist yet but lexically sits under a root is still
	// accepted (the hook may fire before the transcript is flushed).
	fresh := filepath.Join(home, ".claude", "todo", "t.jsonl")
	if _, ok := ValidateTranscriptPath(fresh); !ok {
		t.Fatal("a not-yet-written transcript under a root must be accepted")
	}
}

// Review round 2 (P2-C): a session launched under a [conductors.<name>.claude]
// or [groups."<path>".claude] config_dir that is NOT also a profile slot
// reports its transcript under that dir (directly, or through a worker-scratch
// home whose `projects` symlinks into it). Both the hook handler and the
// daemon rescan must accept it; an unconfigured sibling stays rejected.
func TestValidateTranscriptPath_AcceptsConductorAndGroupConfigDirs(t *testing.T) {
	home := transcriptRootsHome(t)
	writeTranscriptRootsConfig(t, home, `
[profiles.work.claude]
config_dir = '~/.claude-work'
[conductors.coordinator.claude]
config_dir = '~/.claude-coordinator'
[groups."team-a".claude]
config_dir = '~/.claude-team-a'
`)
	for _, dir := range []string{".claude-coordinator", ".claude-team-a"} {
		path := filepath.Join(home, dir, "projects", "-tmp-p", "t.jsonl")
		writeDoneTranscriptAt(t, path)
		if cleaned, ok := ValidateTranscriptPath(path); !ok || cleaned != path {
			t.Fatalf("transcript under configured %s must be accepted: ok=%v cleaned=%q", dir, ok, cleaned)
		}
	}
	if _, ok := ValidateTranscriptPath(filepath.Join(home, ".claude-team-b", "projects", "t.jsonl")); ok {
		t.Fatal("an unconfigured sibling dir must be rejected")
	}

	// The live layout: the conductor's scratch home links `projects` into the
	// conductor config dir, which is no profile slot. The daemon (no
	// CLAUDE_CONFIG_DIR) must still resolve the real location under a root.
	realProjects := filepath.Join(home, ".claude-coordinator", "projects")
	scratch := filepath.Join(workerScratchDirRoot(), "conductor-1", "generation-7")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realProjects, filepath.Join(scratch, "projects")); err != nil {
		t.Fatal(err)
	}
	reported := filepath.Join(scratch, "projects", "-tmp-p", "t.jsonl")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	cleaned, ok := ValidateTranscriptPath(reported)
	if !ok {
		t.Fatal("daemon rescan must accept a scratch path resolving into the conductor config dir")
	}
	if sig, found, pending := ScanTranscriptTailForDone(cleaned); !found || pending || sig.Status != "ok" {
		t.Fatalf("sentinel not detected: found=%v pending=%v sig=%+v", found, pending, sig)
	}
}
