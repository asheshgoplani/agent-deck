//go:build opbench

// Built only with -tags opbench (bench/ops/run.sh, make bench-ops): the
// operation benchmarks start real processes and tmux servers and must stay
// out of plain `go test -bench .` runs such as CI's advisory bench job.

package web

// Operation benchmark for the web UI menu (ASK-106): GET /api/menu through
// the real handler and the default SessionDataService, which opens state.db,
// loads N sessions, refreshes their status against N live shell panes on a
// private tmux socket and encodes the menu. Stable APIs only, so
// bench/ops/run.sh can copy it into older release worktrees.
//
// Run: deck-test -run '^$' -bench '^BenchmarkOp_' -benchmem -count=5 ./internal/web/

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

var opbWebSeq atomic.Int64

// opbWebTmux starts one shell pane per name on a private socket, made the
// process default for the benchmark, and kills only that server afterwards.
func opbWebTmux(b *testing.B, socket string, names []string) {
	b.Helper()
	b.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	for i := 0; i < len(names); i += 50 {
		args := []string{"-L", socket}
		for j := i; j < len(names) && j < i+50; j++ {
			if j > i {
				args = append(args, ";")
			}
			args = append(args, "new-session", "-d", "-s", names[j], "-x", "120", "-y", "40", "/bin/sh")
		}
		if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
			b.Fatalf("tmux new-session: %v %s", err, out)
		}
	}
	prev := tmux.DefaultSocketName()
	tmux.SetDefaultSocketName(socket)
	b.Cleanup(func() { tmux.SetDefaultSocketName(prev) })
}

func opbWebSeed(b *testing.B, n int) string {
	b.Helper()
	if os.Getenv("AGENT_DECK_TEST_HOME_ISOLATED") != "1" || os.Getenv("AGENT_DECK_TEST_ISOLATED") != "1" {
		b.Skip("needs the isolated HOME and tmux dir from TestMain")
	}
	b.Setenv("AGENTDECK_SKIP_UPDATE_CHECK", "1")
	b.Setenv("AGENTDECK_SUPPRESS_TMUX_WARNING", "1")
	profile := fmt.Sprintf("opbweb-%d-%d", n, opbWebSeq.Add(1))
	path, err := session.GetDBPathForProfile(profile)
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
	defer db.Close()
	if err := db.Migrate(); err != nil {
		b.Fatal(err)
	}
	tools := []string{"shell", "claude", "codex"}
	statuses := []string{"idle", "waiting", "running", "error"}
	home, _ := os.UserHomeDir()
	socket := fmt.Sprintf("opbw%d-%d", os.Getpid(), opbWebSeq.Load())
	rows := make([]*statedb.InstanceRow, n)
	names := make([]string, n)
	for i := range rows {
		id := fmt.Sprintf("opb-%04d", i)
		rows[i] = &statedb.InstanceRow{
			ID: id, Title: "bench " + id, ProjectPath: home, GroupPath: fmt.Sprintf("work/team-%d", i%10),
			Order: i, Tool: tools[i%3], Status: statuses[i%4], TmuxSession: "agentdeck_" + id,
			TmuxSocketName: socket, CreatedAt: time.Now().Add(-time.Hour), ToolData: json.RawMessage(`{}`),
		}
		names[i] = rows[i].TmuxSession
	}
	if err := db.SaveInstances(rows); err != nil {
		b.Fatal(err)
	}
	opbWebTmux(b, socket, names)
	return profile
}

// BenchmarkOp_WebMenu: one GET /api/menu with N sessions.
func BenchmarkOp_WebMenu(b *testing.B) {
	if _, err := exec.LookPath("tmux"); err != nil {
		b.Skip("tmux not installed")
	}
	for _, n := range []int{100, 500} {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			profile := opbWebSeed(b, n)
			srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: profile})
			handler := srv.Handler()
			get := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, "/api/menu", nil)
				req.Host = "localhost"
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)
				return rr
			}
			b.ReportAllocs()
			b.ResetTimer()
			// No untimed warm-up: at 500 sessions one request takes seconds.
			// The first response is checked after the loop.
			var first *httptest.ResponseRecorder
			for i := 0; i < b.N; i++ {
				rr := get()
				if rr.Code != http.StatusOK {
					b.Fatalf("GET /api/menu = %d: %.300s", rr.Code, rr.Body.String())
				}
				if first == nil {
					first = rr
				}
			}
			b.StopTimer()
			var snap struct {
				TotalSessions int `json:"totalSessions"`
			}
			if err := json.Unmarshal(first.Body.Bytes(), &snap); err != nil || snap.TotalSessions != n {
				b.Fatalf("menu has %d sessions (err %v), want %d", snap.TotalSessions, err, n)
			}
		})
	}
}
