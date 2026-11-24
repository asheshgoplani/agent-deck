package session

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// The TUI deletes a remote session (after its own confirm dialog) by running
// `agent-deck remove <id>` on the remote. `remove` now refuses a running
// session without --force, so the delete passes --force, and falls back to
// the plain form on a remote whose agent-deck predates the flag.
func TestSSHRunnerDeleteSession_PassesForceWithFallback(t *testing.T) {
	cases := []struct {
		name      string
		firstErr  error
		wantCalls [][]string
	}{
		{"current remote", nil, [][]string{{"remove", "abc", "--force"}}},
		{"remote without --force", errors.New("ssh command failed: exit status 1: flag provided but not defined: -force"),
			[][]string{{"remove", "abc", "--force"}, {"remove", "abc"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			r := &SSHRunner{runFn: func(ctx context.Context, args ...string) ([]byte, error) {
				calls = append(calls, append([]string(nil), args...))
				if len(calls) == 1 {
					return nil, tc.firstErr
				}
				return nil, nil
			}}
			if err := r.DeleteSession(context.Background(), "abc"); err != nil {
				t.Fatalf("DeleteSession: %v", err)
			}
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Errorf("remote calls = %v, want %v", calls, tc.wantCalls)
			}
		})
	}
}
