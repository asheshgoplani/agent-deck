//go:build opbench

// Built only with -tags opbench (bench/ops/run.sh, make bench-ops): the
// operation benchmarks start real processes and tmux servers and must stay
// out of plain `go test -bench .` runs such as CI's advisory bench job.

package session

// Operation benchmark for loading the session store (ASK-106): open a
// profile's state.db and load every session with its groups, as the CLI, TUI,
// web server and notify daemon do on each start or pass. Stable APIs only, so
// bench/ops/run.sh can copy it into older release worktrees.
//
// Run: deck-test -run '^$' -bench '^BenchmarkOp_' -benchmem -count=5 ./internal/session/

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

var opbStoreSeq atomic.Int64

// BenchmarkOp_StateDBLoad: NewStorageWithProfile + LoadWithGroups + Close
// over N stored sessions.
func BenchmarkOp_StateDBLoad(b *testing.B) {
	for _, n := range []int{100, 500} {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			if os.Getenv("AGENT_DECK_TEST_HOME_ISOLATED") != "1" {
				b.Skip("needs the isolated HOME from TestMain")
			}
			b.Setenv("AGENTDECK_SKIP_UPDATE_CHECK", "1")
			b.Setenv("AGENTDECK_SUPPRESS_TMUX_WARNING", "1")
			profile := fmt.Sprintf("opbdb-%d-%d", n, opbStoreSeq.Add(1))
			path, err := GetDBPathForProfile(profile)
			if err != nil {
				b.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				b.Fatal(err)
			}
			db, err := statedb.Open(path)
			if err != nil {
				b.Fatal(err)
			}
			if err := db.Migrate(); err != nil {
				b.Fatal(err)
			}
			tools := []string{"shell", "claude", "codex"}
			home, _ := os.UserHomeDir()
			rows := make([]*statedb.InstanceRow, n)
			for i := range rows {
				id := fmt.Sprintf("opb-%04d", i)
				rows[i] = &statedb.InstanceRow{
					ID: id, Title: "bench " + id, ProjectPath: home, GroupPath: fmt.Sprintf("work/team-%d", i%10),
					Order: i, Tool: tools[i%3], Status: "idle", TmuxSession: "agentdeck_" + id,
					TmuxSocketName: "opb-absent", CreatedAt: time.Now().Add(-time.Hour), ToolData: json.RawMessage(`{}`),
				}
			}
			if err := db.SaveInstances(rows); err != nil {
				b.Fatal(err)
			}
			_ = db.Close()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				st, err := NewStorageWithProfile(profile)
				if err != nil {
					b.Fatal(err)
				}
				insts, _, err := st.LoadWithGroups()
				if err != nil || len(insts) != n {
					b.Fatalf("LoadWithGroups: %d sessions, err %v; want %d", len(insts), err, n)
				}
				_ = st.Close()
			}
		})
	}
}
