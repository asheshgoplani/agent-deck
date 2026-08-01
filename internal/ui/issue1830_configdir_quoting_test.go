package ui

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
)

// TestIssue1830_GlobalSearchConfigDirIsQuoted covers the Codex review Medium on
// PR #1830: createSessionFromGlobalSearch built its resume command with
// `CLAUDE_CONFIG_DIR=%s ` from a raw config dir, bypassing the quoting that
// Instance.buildBashExportPrefix applies (instance.go, audit F2). The string it
// builds is stored as inst.Command and ends up inside a `bash -c` payload, so a
// config_dir containing whitespace silently breaks the command and one
// containing ; or $(...) injects.
//
// This pins the technique: the emitted assignment must survive a real shell
// with the value intact, for paths a user can legitimately have (spaces) and
// for hostile ones.
func TestIssue1830_GlobalSearchConfigDirIsQuoted(t *testing.T) {
	for _, bin := range []string{"bash", "printenv"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}

	marker := "/tmp/agentdeck-pwned-1830-ui"
	_ = os.Remove(marker)
	t.Cleanup(func() { _ = os.Remove(marker) })

	dirs := map[string]string{
		"plain":                "/home/u/.claude",
		"with_spaces":          "/home/u/My Claude Dir/.claude",
		"semicolon_chain":      "/home/u/.claude; touch " + marker,
		"command_substitution": "/home/u/$(touch " + marker + ")claude",
		"single_quote":         "/home/u/it's/.claude",
	}

	for name, dir := range dirs {
		t.Run(name, func(t *testing.T) {
			// Exactly the construction the fixed call site uses.
			assignment := fmt.Sprintf("CLAUDE_CONFIG_DIR=%s ", shellescape.Quote(dir))

			// Run it the way it is ultimately run: as a bash -c payload where
			// the assignment prefixes the command.
			//
			// Read the value back with printenv (an EXTERNAL command) rather
			// than expanding "$CLAUDE_CONFIG_DIR" in the same command line: a
			// var-assignment prefix is applied when the command is executed,
			// but parameter expansion on that same line happens BEFORE it, so
			// an in-line expansion reads empty and would fail every case
			// including the benign one. printenv also matches how the real
			// claude process receives the variable -- from its environment.
			out, err := exec.Command("bash", "-c", assignment+"printenv CLAUDE_CONFIG_DIR").Output()
			if err != nil {
				t.Fatalf("bash rejected the assignment for %q: %v", dir, err)
			}
			if got := strings.TrimSuffix(string(out), "\n"); got != dir {
				t.Errorf("config dir mangled by the shell:\n got: %q\nwant: %q", got, dir)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatalf("payload in %q EXECUTED — value reached the shell unquoted", dir)
			}
		})
	}
}

// TestIssue1830_NoRawConfigDirInterpolation guards the specific line the fix
// changed. The round-trip test above pins the technique but would still pass if
// the call site regressed to the raw form, so assert the raw form is absent
// from the source.
func TestIssue1830_NoRawConfigDirInterpolation(t *testing.T) {
	src, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}

	// Matches `CLAUDE_CONFIG_DIR=%s` interpolated directly from a bare
	// identifier (the pre-fix form) rather than through shellescape.Quote.
	raw := regexp.MustCompile(`CLAUDE_CONFIG_DIR=%s[^"]*",\s*[A-Za-z_][A-Za-z0-9_.]*\)`)
	for _, line := range strings.Split(string(src), "\n") {
		if raw.MatchString(line) && !strings.Contains(line, "shellescape.Quote") {
			t.Errorf("home.go interpolates CLAUDE_CONFIG_DIR without shellescape.Quote:\n  %s", strings.TrimSpace(line))
		}
	}
}
