package statedb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWriteStatusIfCurrentPreservesConcurrentStop(t *testing.T) {
	for _, observe := range []bool{false, true} {
		name := "without_observer"
		if observe {
			name = "with_observer"
		}
		t.Run(name, func(t *testing.T) {
			db := newTestDB(t)
			now := time.Now()
			require.NoError(t, db.SaveInstance(&InstanceRow{
				ID: "worker", Title: "worker", ProjectPath: "/p", GroupPath: "g",
				Tool: "shell", Status: "running", TmuxSession: "worker-pane",
				CreatedAt: now, LastAccessed: now,
			}))
			require.NoError(t, db.WriteStatus("worker", "stopped", "shell"))
			var edges []StatusChange
			SetStatusChangeObserver(nil)
			if observe {
				SetStatusChangeObserver(func(edge StatusChange) { edges = append(edges, edge) })
			}
			t.Cleanup(func() { SetStatusChangeObserver(nil) })

			applied, err := db.WriteStatusIfCurrent("worker", "running", "error", "codex")
			require.NoError(t, err)
			require.False(t, applied)
			rows, err := db.ReadAllStatuses()
			require.NoError(t, err)
			require.Equal(t, "stopped", rows["worker"].Status)
			require.Equal(t, "shell", rows["worker"].Tool)
			require.Empty(t, edges, "a rejected sample must not publish an edge")

			applied, err = db.WriteStatusIfCurrent("worker", "stopped", "running", "codex")
			require.NoError(t, err)
			require.True(t, applied, "a matching fresh observation may revive the row")
			if observe {
				require.Len(t, edges, 1)
				require.Equal(t, "stopped", edges[0].From)
				require.Equal(t, "running", edges[0].To)
				require.Equal(t, "worker-pane", edges[0].TmuxSession)
			}
			applied, err = db.WriteStatusIfCurrent("worker", "running", "running", "codex")
			require.NoError(t, err)
			require.True(t, applied)
			if observe {
				require.Len(t, edges, 1, "an unchanged verdict has no new edge")
			}
		})
	}
}
