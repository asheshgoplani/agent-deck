package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

func TestStatusScanRows_DropsArchivedDownKeepsAncestors(t *testing.T) {
	archived := time.Unix(1_700_000_000, 0)
	row := func(id, status, parent string, archivedAt time.Time) *statedb.InstanceRow {
		return &statedb.InstanceRow{ID: id, Status: status, ParentSessionID: parent, ArchivedAt: archivedAt}
	}
	rows := []*statedb.InstanceRow{
		row("live", "running", "", time.Time{}),
		row("archived-running", "running", "", archived),
		row("archived-stopped", "stopped", "", archived),
		row("archived-error", "error", "", archived),
		row("stopped-not-archived", "stopped", "", time.Time{}),
		row("grandparent", "stopped", "", archived),
		row("parent", "error", "grandparent", archived),
		row("child", "waiting", "parent", time.Time{}),
		row("cycle-a", "stopped", "cycle-b", archived),
		row("cycle-b", "stopped", "cycle-a", archived),
	}

	var got []string
	for _, r := range statusScanRows(rows) {
		got = append(got, r.ID)
	}
	require.Equal(t, []string{"live", "archived-running", "stopped-not-archived", "grandparent", "parent", "child"}, got)
}
