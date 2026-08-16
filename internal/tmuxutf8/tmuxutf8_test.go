package tmuxutf8_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/tmuxutf8"
)

// TestPrepend_AddsFlagInGlobalPosition: `-u` is a GLOBAL tmux option. Placed
// after the subcommand it is either rejected or silently reinterpreted as that
// subcommand's own `-u` (which means "unset" on set-option/set-environment), so
// position is part of the contract, not a formatting detail.
func TestPrepend_AddsFlagInGlobalPosition(t *testing.T) {
	got := tmuxutf8.Prepend([]string{"-L", "agent-deck", "list-panes", "-a", "-F", "#{pane_title}"})
	want := []string{"-u", "-L", "agent-deck", "list-panes", "-a", "-F", "#{pane_title}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("-u must lead the argv\n got:  %v\n want: %v", got, want)
	}
}

// TestPrepend_EmptyArgs: degenerate input must still yield a usable argv rather
// than panicking or dropping the flag.
func TestPrepend_EmptyArgs(t *testing.T) {
	got := tmuxutf8.Prepend(nil)
	want := []string{"-u"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nil args\n got:  %v\n want: %v", got, want)
	}
}

// TestPrepend_Idempotent: two builders can legitimately compose (the web bridge
// prepends onto an argv it assembled itself). `tmux -u -u list-panes` is
// accepted by tmux, but the duplicate would leak into every argv-shape
// assertion in the repo, so the flag is applied at most once.
func TestPrepend_Idempotent(t *testing.T) {
	once := tmuxutf8.Prepend([]string{"has-session", "-t", "x"})
	twice := tmuxutf8.Prepend(once)
	if !reflect.DeepEqual(once, twice) {
		t.Fatalf("Prepend must be idempotent\n once:  %v\n twice: %v", once, twice)
	}
}

// TestPrepend_DoesNotMutateOrAliasCallerSlice: the tmux argv builders sit on
// the status-poll hot path and pass slices they did not allocate. An
// append-aliasing bug here would rewrite a caller's argv from under it.
func TestPrepend_DoesNotMutateOrAliasCallerSlice(t *testing.T) {
	// Extra capacity is what makes append() alias rather than copy.
	original := make([]string, 3, 8)
	copy(original, []string{"kill-session", "-t", "x"})
	snapshot := append([]string(nil), original...)

	out := tmuxutf8.Prepend(original)
	out[len(out)-1] = "CLOBBERED"

	if !reflect.DeepEqual(original, snapshot) {
		t.Fatalf("Prepend must not alias the caller slice\n after: %v\n want:  %v", original, snapshot)
	}
}

// TestHasFlag_OnlyMatchesGlobalPosition: `-u` after the subcommand is a
// different option with a different meaning ("unset" for set-option /
// set-environment — see internal/tmux/tmux.go's `set-option -u status-left`).
// Reporting those as UTF-8 safe would hide exactly the bug this package exists
// to prevent.
func TestHasFlag_OnlyMatchesGlobalPosition(t *testing.T) {
	if !tmuxutf8.HasFlag([]string{"-u", "-L", "s", "list-panes"}) {
		t.Error("leading -u must be reported as present")
	}
	if tmuxutf8.HasFlag([]string{"-L", "s", "set-option", "-t", "x", "-u", "status-left"}) {
		t.Error("a subcommand's own -u must NOT be reported as the global UTF-8 flag")
	}
	if tmuxutf8.HasFlag(nil) {
		t.Error("nil argv must not report the flag")
	}
}

// TestTmuxFactory_EmitsUTF8Flag is the choke-point contract (#1867): the
// exported argv factory in internal/tmux — the one every call site outside the
// package is required to funnel through by
// TestNoRawTmuxExec_OutsideAllowlist — must emit `-u`, so a call site added
// tomorrow is UTF-8 safe without its author knowing this bug exists.
//
// It lives here rather than in internal/tmux so that it can be run without
// starting that package's test binary; the flag's presence is observable
// through the public API because Exec returns the *exec.Cmd unstarted.
func TestTmuxFactory_EmitsUTF8Flag(t *testing.T) {
	cases := []struct {
		name   string
		socket string
		args   []string
		want   []string
	}{
		{
			name:   "default socket",
			socket: "",
			args:   []string{"list-panes", "-a", "-F", "#{pane_title}"},
			want:   []string{"tmux", "-u", "list-panes", "-a", "-F", "#{pane_title}"},
		},
		{
			name:   "isolated socket",
			socket: "agent-deck",
			args:   []string{"list-panes", "-a", "-F", "#{pane_title}"},
			want:   []string{"tmux", "-u", "-L", "agent-deck", "list-panes", "-a", "-F", "#{pane_title}"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tmux.Exec(tc.socket, tc.args...); !reflect.DeepEqual(got.Args, tc.want) {
				t.Errorf("tmux.Exec\n got:  %v\n want: %v", got.Args, tc.want)
			}
			if got := tmux.ExecContext(context.Background(), tc.socket, tc.args...); !reflect.DeepEqual(got.Args, tc.want) {
				t.Errorf("tmux.ExecContext\n got:  %v\n want: %v", got.Args, tc.want)
			}
		})
	}
}

// TestTmuxFactory_UTF8FlagPrecedesSocketSelector pins the ordering explicitly.
// `tmux -L name -u …` also works, but `tmux -L -u name …` (a flag landing
// between -L and its value) does not, and an argv builder that appends rather
// than prepends is one refactor away from that. Assert the exact shape.
func TestTmuxFactory_UTF8FlagPrecedesSocketSelector(t *testing.T) {
	args := tmux.Exec("agent-deck", "has-session", "-t", "x").Args
	if len(args) < 4 || args[0] != "tmux" || args[1] != "-u" || args[2] != "-L" || args[3] != "agent-deck" {
		t.Fatalf("want argv to start with [tmux -u -L agent-deck]; got %v", args)
	}
}
