package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsValidSessionColor locks the CLI-boundary validation contract for
// issue #391. Hex truecolor, ANSI-256 index, and empty-string-to-clear are
// accepted; anything else is rejected so bad typos never reach the render
// layer where they'd fall through to lipgloss defaults with surprising
// results.
func TestIsValidSessionColor(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty_clears_opt_out", "", true},
		{"truecolor_lower", "#ff00aa", true},
		{"truecolor_upper", "#FFA000", true},
		{"truecolor_mixed", "#aB12Cd", true},
		{"ansi_zero", "0", true},
		{"ansi_203", "203", true},
		{"ansi_255", "255", true},
		{"ansi_256_out_of_range", "256", false},
		{"ansi_999_out_of_range", "999", false},
		{"hex_too_short", "#fff", false},
		{"hex_too_long", "#ff00aabb", false},
		{"hex_non_hex_char", "#gg00aa", false},
		{"hex_missing_hash", "ff00aa", false},
		{"named_color_rejected", "red", false},
		{"whitespace_must_be_trimmed_by_caller", " #ff00aa ", false},
		{"sign_rejected", "-1", false},
		{"decimal_rejected", "12.5", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidSessionColor(tc.in); got != tc.want {
				t.Fatalf("isValidSessionColor(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestSessionSetColor_PersistsValidAndRejectsInvalid exercises the full CLI
// boundary (issue #391):
//
//  1. Create a session.
//  2. `session set <id> color "#ff00aa"` must succeed and persist through
//     the sessions.json round-trip.
//  3. `session set <id> color "not-a-color"` must fail with a non-zero
//     exit code and leave the previously-set value intact.
//  4. `session set <id> color ""` must succeed (opt-out) and remove the
//     value from the persisted JSON (omitempty).
//
// Failure mode on main (before this PR): step 2 exits with
// "invalid field: color" at session_cmd.go:888-897 because color is not
// in the validFields map.
func TestSessionSetColor_PersistsValidAndRejectsInvalid(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"add",
		"-t", "color-test",
		"-c", "claude",
		"--no-parent",
		"--json",
		projectDir,
	)
	if code != 0 {
		t.Fatalf("agent-deck add failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var addResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &addResp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}

	// Step 2: valid color persists.
	stdout, stderr, code = runAgentDeck(t, home,
		"session", "set", addResp.ID, "color", "#ff00aa", "--json",
	)
	if code != 0 {
		t.Fatalf("session set color '#ff00aa' failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if !strings.Contains(listJSON, `"color": "#ff00aa"`) {
		t.Fatalf("color '#ff00aa' did not persist in sessions.json:\n%s", listJSON)
	}

	// Step 3: invalid color rejected; previous value intact.
	stdout, stderr, code = runAgentDeck(t, home,
		"session", "set", addResp.ID, "color", "not-a-color", "--json",
	)
	if code == 0 {
		t.Fatalf("session set color 'not-a-color' unexpectedly succeeded\nstdout: %s", stdout)
	}
	if !strings.Contains(stderr, "invalid color") && !strings.Contains(stdout, "invalid color") {
		t.Errorf("expected 'invalid color' diagnostic, stderr=%q stdout=%q", stderr, stdout)
	}
	listJSON = readSessionsJSON(t, home)
	if !strings.Contains(listJSON, `"color": "#ff00aa"`) {
		t.Errorf("rejected color unexpectedly disturbed prior value:\n%s", listJSON)
	}

	// Step 4: empty clears (opt-out).
	stdout, stderr, code = runAgentDeck(t, home,
		"session", "set", addResp.ID, "color", "", "--json",
	)
	if code != 0 {
		t.Fatalf("session set color '' (clear) failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	listJSON = readSessionsJSON(t, home)
	if strings.Contains(listJSON, `"color":`) || strings.Contains(listJSON, `"color": `) {
		t.Errorf("empty-string clear should have removed 'color' (omitempty), still present:\n%s", listJSON)
	}
}
