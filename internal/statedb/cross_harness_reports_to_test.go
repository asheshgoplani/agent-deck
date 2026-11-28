package statedb

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// PR #3166 review F1: a worker whose tool_data reports_to names the source
// depends on it like a tree child. The commit-time guard must refuse to
// archive the source while such a worker exists.
func TestCommitCrossHarnessSupersession_RejectsReportsToDependent(t *testing.T) {
	db := newTestDB(t)
	source := supersessionRow("source", "claude", "personal", "claude", "claude-source", false)
	target := supersessionRow("target", "codex", "work", "codex", "codex-target", true)
	require.NoError(t, db.SaveInstance(source))
	require.NoError(t, db.SaveInstance(target))
	require.NoError(t, db.SaveInstance(&InstanceRow{ID: "worker", Title: "worker", Tool: "claude", ProjectPath: "/project", Command: "claude",
		ToolData: json.RawMessage(`{"reports_to":"source"}`)}))
	require.Equal(t, "source", crossHarnessLineage(rowByID(t, db, "worker").ToolData, "reports_to"), "fixture must persist reports_to")

	_, _, err := db.CommitCrossHarnessSupersession(supersessionIdentity(source), supersessionIdentity(target), time.Now().UTC())
	require.ErrorContains(t, err, "dependent worker worker reports to it")
	require.True(t, rowByID(t, db, source.ID).ArchivedAt.IsZero())
	require.False(t, rowByID(t, db, target.ID).ArchivedAt.IsZero())
}
