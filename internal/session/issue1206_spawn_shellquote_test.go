package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Security: spawn-command shell-quoting (audit F1/F2, sec-exec-REPORT.md).
//
// Non-sandbox claude sessions are dispatched through bashCWrap, which wraps the
// assembled command as `bash -c '<command>'`. The OUTER send-keys shell sees one
// word, but an INNER bash re-parses <command> — so every interpolated token must
// be shell-quoted independently. buildClaudeExtraFlags quotes --model and
// ExtraArgs but historically left --add-dir paths, --channels, and the
// CLAUDE_CONFIG_DIR export raw. A directory (or config dir) named `$(...)` then
// executes at spawn time. These tests pin the quoting.

// TestBuildClaudeExtraFlags_AddDirParentPathIsShellQuoted covers F1: the
// parent-project --add-dir path must be shell-quoted so a command substitution
// in the directory name is NOT evaluated by the inner bash.
func TestBuildClaudeExtraFlags_AddDirParentPathIsShellQuoted(t *testing.T) {
	channelsTestEnv(t)

	const evil = "/repo/$(touch /tmp/agentdeck_pwned)"
	inst := NewInstanceWithTool("addddir-parent", t.TempDir(), "claude")
	inst.ParentProjectPath = evil

	flags := inst.buildClaudeExtraFlags(&ClaudeOptions{})

	if strings.Contains(flags, "--add-dir "+evil) {
		t.Fatalf("parent --add-dir path is interpolated RAW (shell injection F1); got:\n%s", flags)
	}
	if !strings.Contains(flags, "'"+evil+"'") {
		t.Fatalf("expected parent --add-dir path single-quoted, got:\n%s", flags)
	}
}

// TestBuildClaudeExtraFlags_AddDirMultiRepoPathIsShellQuoted covers the second
// F1 site: multi-repo --add-dir paths discovered from AdditionalPaths.
func TestBuildClaudeExtraFlags_AddDirMultiRepoPathIsShellQuoted(t *testing.T) {
	channelsTestEnv(t)

	const evil = "/extra/`reboot`/repo"
	inst := NewInstanceWithTool("addddir-multi", t.TempDir(), "claude")
	inst.MultiRepoEnabled = true
	inst.AdditionalPaths = []string{evil}

	flags := inst.buildClaudeExtraFlags(&ClaudeOptions{})

	if strings.Contains(flags, "--add-dir "+evil) {
		t.Fatalf("multi-repo --add-dir path is interpolated RAW (shell injection F1); got:\n%s", flags)
	}
	if !strings.Contains(flags, "'"+evil+"'") {
		t.Fatalf("expected multi-repo --add-dir path single-quoted, got:\n%s", flags)
	}
}

// TestBuildClaudeExtraFlags_ChannelsValueIsShellQuoted covers the --channels
// arm of F1: the joined channel CSV must be quoted.
func TestBuildClaudeExtraFlags_ChannelsValueIsShellQuoted(t *testing.T) {
	channelsTestEnv(t)

	inst := NewInstanceWithTool("ch-quote", t.TempDir(), "claude")
	setChannelsField(t, inst, []string{"plugin:telegram@a/b", "$(id)"})

	flags := inst.buildClaudeExtraFlags(&ClaudeOptions{})

	if strings.Contains(flags, "--channels plugin:telegram@a/b,$(id)") {
		t.Fatalf("--channels value is interpolated RAW (shell injection F1); got:\n%s", flags)
	}
	if !strings.Contains(flags, "'plugin:telegram@a/b,$(id)'") {
		t.Fatalf("expected --channels value single-quoted, got:\n%s", flags)
	}
}

// TestBuildBashExportPrefix_ConfigDirIsShellQuoted covers F2: the
// CLAUDE_CONFIG_DIR export value must be shell-quoted, like the sibling
// AGENTDECK_RESOLVED_* exports already are.
func TestBuildBashExportPrefix_ConfigDirIsShellQuoted(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	origProfile := os.Getenv("AGENTDECK_PROFILE")
	origClaudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origProfile != "" {
			_ = os.Setenv("AGENTDECK_PROFILE", origProfile)
		} else {
			_ = os.Unsetenv("AGENTDECK_PROFILE")
		}
		if origClaudeDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origClaudeDir)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})

	_ = os.Setenv("HOME", tmpHome)
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	_ = os.Unsetenv("AGENTDECK_PROFILE")

	agentDeckDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Explicit config_dir whose path contains a shell metacharacter; the
	// installer/user could legally have such a directory. Without quoting the
	// `;` (or `$()`) injects into the bash -c payload.
	cfg := "[claude]\nconfig_dir = \"~/.claude;touch_pwned\"\n"
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstanceWithGroupAndTool("cfgdir-quote", "/tmp/p", "code", "claude")
	if !IsClaudeConfigDirExplicitForInstance(inst) {
		t.Fatalf("precondition: explicit config_dir must resolve for this fixture")
	}

	prefix := inst.buildBashExportPrefix()

	if !strings.Contains(prefix, "CLAUDE_CONFIG_DIR=") {
		t.Fatalf("precondition: CLAUDE_CONFIG_DIR must be exported; got:\n%s", prefix)
	}
	// RAW interpolation would emit `export CLAUDE_CONFIG_DIR=<path>;touch_pwned; `.
	if strings.Contains(prefix, "CLAUDE_CONFIG_DIR="+filepath.Join(tmpHome, ".claude;touch_pwned")) {
		t.Fatalf("CLAUDE_CONFIG_DIR exported RAW (shell injection F2); got:\n%s", prefix)
	}
	// Quoted form: a single quote immediately follows the `=`.
	if !strings.Contains(prefix, "CLAUDE_CONFIG_DIR='") {
		t.Fatalf("expected CLAUDE_CONFIG_DIR export single-quoted, got:\n%s", prefix)
	}
}
