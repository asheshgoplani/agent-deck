package web

import (
	"reflect"
	"strings"
	"testing"
)

func TestTmuxAttachCommandUsesIgnoreSizeFlag(t *testing.T) {
	t.Setenv("TMUX", "")

	cmd := tmuxAttachCommand("sess-1")

	wantArgs := []string{"tmux", "attach-session", "-f", "ignore-size", "-t", "sess-1"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("unexpected args: got %v want %v", cmd.Args, wantArgs)
	}
}

func TestTmuxAttachCommandUsesSocketFromTMUXEnv(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-test.sock,12345,0")

	cmd := tmuxAttachCommand("sess-2")

	wantArgs := []string{"tmux", "-S", "/tmp/tmux-test.sock", "attach-session", "-f", "ignore-size", "-t", "sess-2"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("unexpected args with TMUX env: got %v want %v", cmd.Args, wantArgs)
	}

	for _, env := range cmd.Env {
		if strings.HasPrefix(env, "TMUX=") {
			t.Fatalf("TMUX variable should be removed from command env, got %q", env)
		}
	}
}
