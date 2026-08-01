package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestIssue1830_ResumeSessionRejectsNonUUID covers the Codex review finding on
// PR #1830: both CLI entry points that accept --resume-session
// (handleAdd in main.go and runLaunchCommand in launch_cmd.go) pass the raw
// flag value to session.MarkClaudeSessionIDVerified — vouching for it as an
// ownership declaration — and it is later interpolated into
// `--session-id "%s"`, a DOUBLE-QUOTED shell context where $(...) still
// performs command substitution. Only the TUI validated its input
// (internal/ui/home.go via IsBareClaudeSessionUUID); the CLI did not.
//
// The fix refuses non-UUID values outright at both sites rather than
// sanitizing-and-continuing or falling back to an unverified path. These
// tests run the real binary so a validation block accidentally placed after
// an early os.Exit (or dropped in a refactor) fails the test rather than
// silently passing a source-level assertion.
func TestIssue1830_ResumeSessionRejectsNonUUID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in short mode")
	}

	binPath := filepath.Join(t.TempDir(), "agent-deck-resume-validation")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\noutput: %s", err, out)
	}

	// Payloads that must never be vouched for. The command-substitution and
	// metacharacter cases are the security core of the finding; the rest
	// guard the "operator typed anything" looseness the fix closes.
	payloads := map[string]string{
		"command_substitution":  `$(touch /tmp/agentdeck-pwned-1830)`,
		"backtick_substitution": "`id`",
		"quote_break_and_chain": `x"; touch /tmp/agentdeck-pwned-1830; echo "`,
		"semicolon_chain":       `abc; id`,
		"uuid_with_suffix":      `91fd7978-1a2b-3c4d-5e6f-7a8b9c0d1e2f.jsonl`,
		"uuid_with_whitespace":  ` 91fd7978-1a2b-3c4d-5e6f-7a8b9c0d1e2f `,
		"uppercase_uuid":        `91FD7978-1A2B-3C4D-5E6F-7A8B9C0D1E2F`,
		"not_a_uuid":            `latest`,
	}

	// Both subcommands that expose the flag. Each is invoked with -c claude so
	// the pre-existing tool check passes and execution reaches the new guard.
	commands := map[string][]string{
		"add":    {"add", ".", "-c", "claude", "--resume-session"},
		"launch": {"launch", ".", "-c", "claude", "--resume-session"},
	}

	for cmdName, argv := range commands {
		for payloadName, payload := range payloads {
			t.Run(cmdName+"/"+payloadName, func(t *testing.T) {
				tmpHome := t.TempDir()
				projectDir := t.TempDir()

				args := append(append([]string{}, argv...), payload)
				cmd := exec.Command(binPath, args...)
				cmd.Dir = projectDir
				cmd.Env = sandboxedCLIEnv(tmpHome)

				out, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatalf("%s accepted an invalid --resume-session value %q "+
						"(expected refusal); output: %s", cmdName, payload, out)
				}

				// The refusal must name the flag and the expected form so the
				// operator can correct it, per the fix's stated contract.
				lower := strings.ToLower(string(out))
				if !strings.Contains(lower, "--resume-session") {
					t.Errorf("refusal for %q does not name the flag; output: %s", payload, out)
				}
				if !strings.Contains(lower, "uuid") {
					t.Errorf("refusal for %q does not name the expected form; output: %s", payload, out)
				}

				// Defense in depth: the payload must not have executed.
				if _, statErr := os.Stat("/tmp/agentdeck-pwned-1830"); statErr == nil {
					_ = os.Remove("/tmp/agentdeck-pwned-1830")
					t.Fatalf("payload %q EXECUTED — command substitution reached a shell", payload)
				}
			})
		}
	}
}

// TestIssue1830_ResumeSessionAcceptsBareUUID pins the other half of the
// contract: the guard must not reject the well-formed ids it exists to let
// through. Asserted against the shared predicate both CLI sites now call, so
// this stays a pure unit check with no session creation.
func TestIssue1830_ResumeSessionAcceptsBareUUID(t *testing.T) {
	valid := []string{
		"91fd7978-1a2b-3c4d-5e6f-7a8b9c0d1e2f",
		"00000000-0000-0000-0000-000000000000",
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
	}
	for _, id := range valid {
		if !session.IsBareClaudeSessionUUID(id) {
			t.Errorf("IsBareClaudeSessionUUID(%q) = false, want true", id)
		}
	}
}

// sandboxedCLIEnv builds an environment pointing HOME and every XDG root at a
// throwaway dir so the CLI under test can never read or write the real
// ~/.agent-deck (see the repo CLAUDE.md data-loss rules). TMUX*/AGENTDECK_*
// are stripped so the nested-session guard in main.go does not early-exit
// before the flag validation being tested.
func sandboxedCLIEnv(tmpHome string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX") ||
			strings.HasPrefix(kv, "AGENTDECK_") ||
			strings.HasPrefix(kv, "HOME=") ||
			strings.HasPrefix(kv, "XDG_") ||
			strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+tmpHome,
		"XDG_CONFIG_HOME="+filepath.Join(tmpHome, ".config"),
		"XDG_DATA_HOME="+filepath.Join(tmpHome, ".local", "share"),
		"XDG_CACHE_HOME="+filepath.Join(tmpHome, ".cache"),
		"AGENTDECK_PROFILE=test-1830",
		"TERM=dumb",
	)
}
