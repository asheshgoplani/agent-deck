package session

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// A spawn whose pane died at once (#2099 read-back) is a session_start
// failure; a tool that was not found is reported as tool_not_found.
func TestSpawnFailureKind(t *testing.T) {
	cases := []struct {
		err  error
		want telemetry.ErrKind
	}{
		{&SpawnFailedError{TmuxName: "x", Record: &SpawnFailureRecord{Reason: spawnToolNotFoundPrefix + "claude"}}, telemetry.KindToolNotFound},
		{&SpawnFailedError{TmuxName: "x", Record: &SpawnFailureRecord{Reason: "spawn_died_fast"}}, telemetry.KindOther},
		{&SpawnFailedError{TmuxName: "x"}, telemetry.KindOther},
	}
	for _, c := range cases {
		if got := startErrKind(c.err); got != c.want {
			t.Errorf("startErrKind(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
