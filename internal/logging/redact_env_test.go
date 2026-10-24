package logging

import (
	"strings"
	"testing"
)

// TestRedactCommandEnv_HidesValuesKeepsKeys pins LOC-4: a logged launch or
// restart command keeps the variable NAMES but never the values, for every
// shape the command builders emit (export with backslash-quote escapes,
// bare KEY=v prefixes, tmux -e KEY=v, and the '"'"' re-quoting of a bash -c wrapper).
func TestRedactCommandEnv_HidesValuesKeepsKeys(t *testing.T) {
	const secret = "sk-live-SECRET42"
	cases := []struct {
		name string
		in   string
		keys []string
	}{
		{"export single quoted", "export API_KEY='" + secret + "' && exec claude", []string{"export API_KEY=***"}},
		{"export with escaped quote", "export MSG='it'\\''s " + secret + "' && claude", []string{"export MSG=***"}},
		{"bare prefix", "FOO=" + secret + " BAR=\"" + secret + " x\" claude --resume abc", []string{"FOO=***", "BAR=***"}},
		{"tmux -e", "-e TOKEN=" + secret, []string{"TOKEN=***"}},
		{"bash -c rewrap", `bash -c 'export K='"'"'` + secret + `'"'"' && claude'`, []string{"export K=***"}},
		{"semicolon join", "export A='" + secret + "'; export B=" + secret + "; claude", []string{"export A=***", "export B=***"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactCommandEnv(tc.in)
			if strings.Contains(got, secret) || strings.Contains(got, "SECRET42") {
				t.Fatalf("RedactCommandEnv leaked the value:\n in: %s\nout: %s", tc.in, got)
			}
			for _, k := range tc.keys {
				if !strings.Contains(got, k) {
					t.Errorf("RedactCommandEnv(%q) = %q, want it to keep %q", tc.in, got, k)
				}
			}
		})
	}
}

// TestRedactCommandEnv_LeavesOrdinaryArgs keeps the log useful: flags,
// paths and session ids that are not env assignments stay readable.
func TestRedactCommandEnv_LeavesOrdinaryArgs(t *testing.T) {
	in := "cd -- '/home/u/a=b' && exec claude --resume 1234-abcd --model=opus"
	if got := RedactCommandEnv(in); got != in {
		t.Fatalf("RedactCommandEnv changed a command with no env assignment:\n in: %s\nout: %s", in, got)
	}
	args := RedactCommandEnvArgs([]string{"respawn-pane", "-k", "-t", "s:", "-e", "K=v1", "/bin/bash", "-lc", "export X='v2' && run"})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "v1") || strings.Contains(joined, "v2") {
		t.Fatalf("RedactCommandEnvArgs leaked a value: %q", joined)
	}
	if args[0] != "respawn-pane" || args[3] != "s:" || args[4] != "-e" || args[5] != "K=***" {
		t.Fatalf("RedactCommandEnvArgs mangled non-env args: %q", args)
	}
}
