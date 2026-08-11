package testutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestTmuxAlreadyIsolatedRejectsUnbackedMarker is the guard for the sharpest
// edge in this helper family: TmuxAlreadyIsolated is used to DISABLE tmux
// isolation, so anything that convinces it wrongly points the whole package at
// the user's real tmux server.
//
// The marker env var alone is not evidence. It survives in any inherited
// environment — a developer's exported shell var, a stale value in a parent
// agent-deck session, an env captured before cleanup ran. If it were trusted on
// its own, cmd/agent-deck would skip IsolateTmuxSocket and then run
// cleanupTestSessions (`tmux list-sessions` + `kill-session`) against the
// DEFAULT socket: the 2026-04-17 incident, where `go test ./...` killed every
// session in the user's live profile.
//
// So the claim must be corroborated by the value it claims to be about.
func TestTmuxAlreadyIsolatedRejectsUnbackedMarker(t *testing.T) {
	privateDir := t.TempDir()

	cases := []struct {
		name       string
		marker     string
		tmuxTmpdir string
		setTmpdir  bool
		tmux       string
		want       bool
		why        string
	}{
		{
			name: "no marker", marker: "", setTmpdir: true, tmuxTmpdir: privateDir,
			want: false, why: "absent marker must never report isolated",
		},
		{
			name: "marker but TMUX_TMPDIR unset", marker: "1", setTmpdir: false,
			want: false,
			why: "an unset TMUX_TMPDIR falls back to tmux's default base, so the " +
				"process can reach the user's live server",
		},
		{
			name: "marker but TMUX_TMPDIR empty", marker: "1", setTmpdir: true, tmuxTmpdir: "",
			want: false, why: "an empty value is the same fallback as unset",
		},
		{
			name: "marker but TMUX_TMPDIR is /tmp", marker: "1", setTmpdir: true, tmuxTmpdir: "/tmp",
			want: false, why: "/tmp IS tmux's default socket base, not a private dir",
		},
		{
			name: "marker but TMUX_TMPDIR is os.TempDir", marker: "1", setTmpdir: true,
			tmuxTmpdir: os.TempDir(),
			want:       false, why: "the tmp root is the default base",
		},
		{
			name: "marker but TMUX_TMPDIR is a tmux-<uid> dir", marker: "1", setTmpdir: true,
			tmuxTmpdir: filepath.Join(privateDir, "tmux-501"),
			want:       false, why: "a tmux-<uid> dir is the default base one level in",
		},
		{
			name: "marker but TMUX_TMPDIR is the torn-down sentinel", marker: "1", setTmpdir: true,
			tmuxTmpdir: "/dev/null/agent-deck-tmux-isolation-torn-down",
			want:       false,
			why: "cleanup already ran and parked the value; isolation is OVER, so a " +
				"child must isolate afresh rather than inherit a dead dir",
		},
		{
			name: "marker and private dir but TMUX set", marker: "1", setTmpdir: true,
			tmuxTmpdir: privateDir, tmux: "/private/tmp/tmux-501/default,12345,0",
			want: false,
			why: "tmux resolves $TMUX BEFORE TMUX_TMPDIR, so an inherited TMUX reaches " +
				"the user's server no matter what TMUX_TMPDIR says (2026-04-17 cascade)",
		},
		{
			name: "marker and private dir", marker: "1", setTmpdir: true, tmuxTmpdir: privateDir,
			want: true,
			why: "a corroborated claim must still be honoured, or re-exec'd helpers " +
				"isolate again and strand an ad-tmux-* dir per invocation",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(testutil.TestIsolationMarkerEnv, tc.marker)
			if tc.setTmpdir {
				t.Setenv("TMUX_TMPDIR", tc.tmuxTmpdir)
			} else {
				t.Setenv("TMUX_TMPDIR", "")
				if err := os.Unsetenv("TMUX_TMPDIR"); err != nil {
					t.Fatalf("unset TMUX_TMPDIR: %v", err)
				}
			}
			t.Setenv("TMUX", tc.tmux)
			if tc.tmux == "" {
				if err := os.Unsetenv("TMUX"); err != nil {
					t.Fatalf("unset TMUX: %v", err)
				}
			}

			if got := testutil.TmuxAlreadyIsolated(); got != tc.want {
				t.Errorf("TmuxAlreadyIsolated() = %v, want %v\n%s", got, tc.want, tc.why)
			}
		})
	}
}
