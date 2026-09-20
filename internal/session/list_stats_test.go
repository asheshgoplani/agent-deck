package session

import (
	"context"
	"testing"
)

func TestParseListStats(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   *ListStats
	}{
		{
			name:   "well-formed line",
			stderr: ListStatsPrefix + `{"status_pass_ms":46812,"tmux_calls":312,"sessions":76}`,
			want:   &ListStats{StatusPassMS: 46812, TmuxCalls: 312, Sessions: 76},
		},
		{
			name:   "line among other stderr chatter",
			stderr: "some tmux warning\n" + ListStatsPrefix + `{"status_pass_ms":5,"tmux_calls":2,"sessions":1}` + "\nmore noise",
			want:   &ListStats{StatusPassMS: 5, TmuxCalls: 2, Sessions: 1},
		},
		{
			name:   "absent — older remote or not asked",
			stderr: "",
			want:   nil,
		},
		{
			name:   "malformed payload is skipped, not fatal",
			stderr: ListStatsPrefix + `{not json`,
			want:   nil,
		},
		{
			name:   "unrelated stderr only",
			stderr: "ssh_command_stderr: something else entirely\n",
			want:   nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseListStats([]byte(tc.stderr))
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("parseListStats() = %+v, want %+v", got, tc.want)
			}
			if got != nil && *got != *tc.want {
				t.Fatalf("parseListStats() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestSSHRunner_FetchSessions_StatsBestEffort covers #2331's controller-side
// half: a remote that answers with a list-stats line gets it parsed and
// returned; an older remote (or a malformed line) still yields the sessions
// normally with a nil *ListStats, never an error.
func TestSSHRunner_FetchSessions_StatsBestEffort(t *testing.T) {
	sessionsJSON := []byte(`[{"id":"s1","title":"t","status":"waiting"}]`)

	t.Run("stats present", func(t *testing.T) {
		runner := &SSHRunner{fetchSessionsFn: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			return sessionsJSON, []byte(ListStatsPrefix + `{"status_pass_ms":46812,"tmux_calls":312,"sessions":76}`), nil
		}}
		sessions, stats, err := runner.FetchSessions(context.Background())
		if err != nil {
			t.Fatalf("FetchSessions: %v", err)
		}
		if len(sessions) != 1 {
			t.Fatalf("len(sessions)=%d, want 1", len(sessions))
		}
		if stats == nil || stats.StatusPassMS != 46812 || stats.TmuxCalls != 312 || stats.Sessions != 76 {
			t.Fatalf("stats = %+v, want {46812 312 76}", stats)
		}
	})

	t.Run("stats absent — older remote", func(t *testing.T) {
		runner := &SSHRunner{fetchSessionsFn: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			return sessionsJSON, nil, nil
		}}
		sessions, stats, err := runner.FetchSessions(context.Background())
		if err != nil {
			t.Fatalf("FetchSessions: %v", err)
		}
		if len(sessions) != 1 {
			t.Fatalf("len(sessions)=%d, want 1", len(sessions))
		}
		if stats != nil {
			t.Fatalf("stats = %+v, want nil", stats)
		}
	})

	t.Run("stats malformed — still returns sessions", func(t *testing.T) {
		runner := &SSHRunner{fetchSessionsFn: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			return sessionsJSON, []byte(ListStatsPrefix + "{not json"), nil
		}}
		sessions, stats, err := runner.FetchSessions(context.Background())
		if err != nil {
			t.Fatalf("FetchSessions: %v", err)
		}
		if len(sessions) != 1 {
			t.Fatalf("len(sessions)=%d, want 1", len(sessions))
		}
		if stats != nil {
			t.Fatalf("stats = %+v, want nil", stats)
		}
	})
}

// TestSSHRunner_FetchSessions_RealPath_RoutesViaListStatsFlag pins that the
// real (non-test-hook) path always asks for stats, so the flag is not
// forgotten on some future refactor of FetchSessions.
func TestSSHRunner_FetchSessions_RealPath_RoutesViaListStatsFlag(t *testing.T) {
	var gotArgs []string
	runner := &SSHRunner{runFn: func(ctx context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("[]"), nil
	}}
	if _, _, err := runner.FetchSessions(context.Background()); err != nil {
		t.Fatalf("FetchSessions: %v", err)
	}
	want := []string{"list", "--json", ListStatsFlag}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("args = %v, want %v", gotArgs, want)
		}
	}
}
