package tmux

import (
	"reflect"
	"testing"
)

func TestStartCommandSpecRetainsFastExitInSpawnQueue(t *testing.T) {
	s := &Session{
		Name:                       "fast-exit",
		RunCommandAsInitialProcess: true,
		OptionOverrides:            map[string]string{"remain-on-exit": "on"},
	}
	launcher, args := s.startCommandSpec("/tmp", "printf 'answered'\n")
	if launcher != "tmux" {
		t.Fatalf("launcher = %q, want tmux", launcher)
	}
	want := []string{";", "set-option", "-t", s.Name, "remain-on-exit", "on"}
	if len(args) < len(want) || !reflect.DeepEqual(args[len(args)-len(want):], want) {
		t.Fatalf("fast-exit option must follow new-session in the same tmux call: %v", args)
	}
}
