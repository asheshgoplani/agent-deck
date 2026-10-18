package main

import (
	"encoding/json"
	"testing"
)

// Review (docs): an unknown session exits 2 and, with --json, prints the
// same result object as every other failure. /login and /logout are refused
// with their own code before any session is looked up.
func TestSessionCommand_NotFoundAndRefusedEmitResultJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	for _, tc := range []struct {
		args []string
		exit int
		code string
	}{
		{[]string{"session", "command", "no-such-session", "/compact", "--json"}, 2, "not_found"},
		{[]string{"session", "command", "no-such-session", "/logout", "--json"}, 1, "refused_command"},
		{[]string{"session", "command", "no-such-session", "/login", "--json"}, 1, "refused_command"},
	} {
		stdout, stderr, code := runAgentDeck(t, home, tc.args...)
		if code != tc.exit {
			t.Fatalf("%v: exit %d, want %d\nstdout: %s\nstderr: %s", tc.args, code, tc.exit, stdout, stderr)
		}
		var res struct {
			OK   bool   `json:"ok"`
			Code string `json:"code"`
		}
		if err := json.Unmarshal([]byte(stdout), &res); err != nil || res.OK || res.Code != tc.code {
			t.Fatalf("%v: stdout %q (%v), want a result with code %s", tc.args, stdout, err, tc.code)
		}
	}
}
