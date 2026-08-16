//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestTmuxProbeBounded_EmitsExactlyOneLeadingUTF8Flag pins the separate CLI
// probe builder from #1867. These display-message probes deliberately bypass
// internal/tmux's socket-aware factory so they can auto-route through $TMUX;
// consequently this test must fail if tmuxutf8.Prepend is removed here even
// while every internal/tmux factory test remains green.
func TestTmuxProbeBounded_EmitsExactlyOneLeadingUTF8Flag(t *testing.T) {
	fakeBin := t.TempDir()
	fakeTmux := filepath.Join(fakeBin, "tmux")
	if err := os.WriteFile(fakeTmux, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	out, err := tmuxProbeBounded("display-message", "-p", "#{session_name}|#{pane_current_path}")
	if err != nil {
		t.Fatalf("tmuxProbeBounded: %v", err)
	}

	got := strings.Fields(string(out))
	want := []string{"-u", "display-message", "-p", "#{session_name}|#{pane_current_path}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tmuxProbeBounded argv\n got:  %q\n want: %q", got, want)
	}

	utf8Flags := 0
	for _, arg := range got {
		if arg == "-u" {
			utf8Flags++
		}
	}
	if utf8Flags != 1 {
		t.Fatalf("tmuxProbeBounded must emit exactly one -u, got %d in %q", utf8Flags, got)
	}
}
