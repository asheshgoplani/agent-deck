package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// subcommandHelpGaps lists subcommands that do not yet answer --help/-h with
// usage. They are still run and must still leave HOME and tmux untouched,
// but exit status and usage text are not required. This list may only
// shrink: once an entry starts printing usage with exit 0 the test fails and
// asks for the entry to be removed.
var subcommandHelpGaps = map[string]string{
	// Script generators: the output is the completion script, which is
	// read-only by construction. Not a defect.
	"completion bash": "emits the completion script",
	"completion zsh":  "emits the completion script",
	"completion fish": "emits the completion script",

	// Live gaps found when this test was added.
	"profile list":          "runs the listing",
	"watcher import":        "treats the flag as a path to import",
	"watcher install-skill": "treats the flag as a skill name",
	"openclaw status":       "dials the gateway",
	"oc status":             "dials the gateway",
	"worktree trust-hooks":  "flag package prints usage but exits 2",
	"wt trust-hooks":        "flag package prints usage but exits 2",
}

// TestEverySubcommandHelpIsReadOnly closes the gap left by
// TestEveryRegisteredCommandHelpIsReadOnly, which only asks top-level
// commands for help. The escaped defects were one level down: `hooks install
// --help` ran the install and edited Claude settings (#1993, fixed in #2010),
// and the same help-consent hole sat in other dispatchers (#2025). Hand-kept
// lists covered the cases that were reported; this walks completionTree, the
// in-repo mirror of every dispatcher's subcommand switch, so a new subcommand
// is covered the moment it becomes completable.
//
// For each `<command> <subcommand> --help|-h` it asserts: exit 0, usage on
// the output, no file created under the sandboxed HOME/XDG dirs, and no tmux
// session created (a PATH shim records every tmux invocation).
func TestEverySubcommandHelpIsReadOnly(t *testing.T) {
	shimDir := t.TempDir()
	tmuxLog := filepath.Join(shimDir, "tmux.log")
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + tmuxLog + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(shimDir, "tmux"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	seenGaps := map[string]bool{}
	for _, node := range completionTree {
		for _, sub := range node.subs {
			key := node.name + " " + sub
			_, isGap := subcommandHelpGaps[key]
			seenGaps[key] = isGap
			for _, shape := range []string{"--help", "-h"} {
				args := []string{node.name, sub, shape}
				t.Run(strings.Join(args, "/"), func(t *testing.T) {
					if err := os.WriteFile(tmuxLog, nil, 0o644); err != nil {
						t.Fatal(err)
					}
					home := t.TempDir()
					out, err := runIssue2025Helper(t, home, args)
					printedHelp := err == nil && strings.Contains(string(out), "Usage")

					assertNoFiles(t, home)
					calls, readErr := os.ReadFile(tmuxLog)
					if readErr != nil {
						t.Fatal(readErr)
					}
					for _, line := range strings.Split(string(calls), "\n") {
						if strings.Contains(line, "new-session") {
							t.Errorf("help request started a tmux session: tmux %s", line)
						}
					}

					switch {
					case isGap && printedHelp:
						t.Errorf("%q now prints usage for %s; remove it from subcommandHelpGaps", key, shape)
					case !isGap && err != nil:
						t.Errorf("help request failed: %v\n%s", err, out)
					case !isGap && !printedHelp:
						t.Errorf("help request did not print usage:\n%s", out)
					}
				})
			}
		}
	}
	for key := range subcommandHelpGaps {
		if !seenGaps[key] {
			t.Errorf("subcommandHelpGaps entry %q is not a subcommand in completionTree; remove it", key)
		}
	}
}
