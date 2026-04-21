package tmux

import (
	"reflect"
	"testing"
)

// TestTmuxArgs_EmptySocketName_LeavesArgsUntouched is the default-socket
// contract (scope decision 1): socket_name="" means zero behavior change
// for existing users. The factory must NOT inject -L in that case.
func TestTmuxArgs_EmptySocketName_LeavesArgsUntouched(t *testing.T) {
	got := tmuxArgs("", "list-sessions", "-F", "#{session_name}")
	want := []string{"list-sessions", "-F", "#{session_name}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("empty socket must pass args through unchanged\n got:  %v\n want: %v", got, want)
	}
}

// TestTmuxArgs_WithSocketName_PrependsDashL is the isolation contract:
// when a socket name is configured, every tmux command must target that
// socket via the tmux `-L <name>` flag placed before the subcommand.
func TestTmuxArgs_WithSocketName_PrependsDashL(t *testing.T) {
	got := tmuxArgs("agent-deck", "has-session", "-t", "foo")
	want := []string{"-L", "agent-deck", "has-session", "-t", "foo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("socket name must be injected as leading -L <name>\n got:  %v\n want: %v", got, want)
	}
}

// TestTmuxArgs_WithSocketName_DoesNotMutateCallerSlice guards against
// append-aliasing bugs. The factory is called in hot paths; a mutation of
// the caller's slice would be a nasty action-at-a-distance regression.
func TestTmuxArgs_WithSocketName_DoesNotMutateCallerSlice(t *testing.T) {
	original := []string{"kill-session", "-t", "x"}
	snapshot := append([]string(nil), original...)
	_ = tmuxArgs("isolated", original...)
	if !reflect.DeepEqual(original, snapshot) {
		t.Fatalf("tmuxArgs must not mutate caller slice\n original after: %v\n expected:       %v", original, snapshot)
	}
}

// TestTmuxArgs_WithSocketName_EmptyArgs handles the degenerate case. A
// bare `tmux -L agent-deck` would print help/exit non-zero; still, the
// factory must not crash or drop the -L flag.
func TestTmuxArgs_WithSocketName_EmptyArgs(t *testing.T) {
	got := tmuxArgs("agent-deck")
	want := []string{"-L", "agent-deck"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("zero-arg call must still inject -L\n got:  %v\n want: %v", got, want)
	}
}

// TestTmuxArgs_WhitespaceOnlySocketName_TreatedAsEmpty: config values are
// user-supplied strings. Whitespace-only is a likely typo — treat as empty
// to avoid silently building `tmux -L " " …` (tmux would accept it and
// create an unreachable server).
func TestTmuxArgs_WhitespaceOnlySocketName_TreatedAsEmpty(t *testing.T) {
	got := tmuxArgs("   ", "list-sessions")
	want := []string{"list-sessions"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("whitespace-only socket must be treated as empty\n got:  %v\n want: %v", got, want)
	}
}

// TestSession_TmuxCmd_EmptySocket_NoDashL: on an un-configured session
// (new install, no config, no CLI flag), tmuxCmd must produce a plain
// `tmux …` invocation — preserving pre-v1.7.50 behavior byte-for-byte.
func TestSession_TmuxCmd_EmptySocket_NoDashL(t *testing.T) {
	s := &Session{Name: "agentdeck-x"}
	cmd := s.tmuxCmd("has-session", "-t", s.Name)
	wantArgs := []string{"tmux", "has-session", "-t", s.Name}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("empty SocketName must produce plain tmux invocation\n got:  %v\n want: %v", cmd.Args, wantArgs)
	}
}

// TestSession_TmuxCmd_WithSocket_PrependsDashL: when the Session was
// created under a configured socket (or a --tmux-socket CLI flag), every
// subsequent tmux call must carry the -L.
func TestSession_TmuxCmd_WithSocket_PrependsDashL(t *testing.T) {
	s := &Session{Name: "agentdeck-x", SocketName: "agent-deck"}
	cmd := s.tmuxCmd("has-session", "-t", s.Name)
	wantArgs := []string{"tmux", "-L", "agent-deck", "has-session", "-t", s.Name}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("non-empty SocketName must inject -L in front of subcommand\n got:  %v\n want: %v", cmd.Args, wantArgs)
	}
}

// TestDefaultSocketName_InitiallyEmpty: zero-config installs must produce
// a blank default so package-level probes (version check, duplicate reap,
// list-all) keep targeting the user's default server — pre-v1.7.50
// behavior.
func TestDefaultSocketName_InitiallyEmpty(t *testing.T) {
	// Reset, in case an earlier test in the same binary wrote it.
	SetDefaultSocketName("")
	if got := DefaultSocketName(); got != "" {
		t.Fatalf("default socket must be empty on fresh install; got %q", got)
	}
}

// TestDefaultSocketName_SetAndGet_Roundtrip: the config-load path stores
// the user's `[tmux].socket_name` once; every later probe must observe it.
func TestDefaultSocketName_SetAndGet_Roundtrip(t *testing.T) {
	t.Cleanup(func() { SetDefaultSocketName("") })
	SetDefaultSocketName("agent-deck")
	if got := DefaultSocketName(); got != "agent-deck" {
		t.Fatalf("default socket round-trip broken; got %q want %q", got, "agent-deck")
	}
}

// TestDefaultSocketName_TrimsWhitespace: guards the same foot-gun as
// tmuxArgs — a stray space in config.toml must not land agent-deck on a
// server named " ".
func TestDefaultSocketName_TrimsWhitespace(t *testing.T) {
	t.Cleanup(func() { SetDefaultSocketName("") })
	SetDefaultSocketName("   agent-deck\t")
	if got := DefaultSocketName(); got != "agent-deck" {
		t.Fatalf("default socket must be trimmed; got %q want %q", got, "agent-deck")
	}
}
