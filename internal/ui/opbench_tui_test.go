//go:build opbench

// Built only with -tags opbench (bench/ops/run.sh, make bench-ops): the
// operation benchmarks start real processes and tmux servers and must stay
// out of plain `go test -bench .` runs such as CI's advisory bench job.

package ui

// Operation benchmarks for the TUI (ASK-106). They drive the production Home
// model in process, like TestPerfFleetUIHarness, against sessions written to
// a real state.db under the HOME that TestMain isolated. Only stable APIs are
// used so bench/ops/run.sh can copy this file into older release worktrees.
//
// Run: deck-test -run '^$' -bench '^BenchmarkOp_' -benchmem -count=5 ./internal/ui/

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	tea "github.com/charmbracelet/bubbletea"
)

var opbProfileSeq atomic.Int64

// opbRequireSandbox refuses to run outside TestMain's isolated HOME and tmux
// directory.
func opbRequireSandbox(b *testing.B) {
	b.Helper()
	if os.Getenv("AGENT_DECK_TEST_HOME_ISOLATED") != "1" || os.Getenv("AGENT_DECK_TEST_ISOLATED") != "1" {
		b.Skip("needs the isolated HOME and tmux dir from TestMain")
	}
	b.Setenv("AGENTDECK_SKIP_UPDATE_CHECK", "1")
	b.Setenv("AGENTDECK_SUPPRESS_TMUX_WARNING", "1")
}

// opbSeedProfile writes n sessions into a fresh profile's state.db and
// returns the profile name and tmux session names.
func opbSeedProfile(b *testing.B, n int, socket string) (string, []string) {
	b.Helper()
	opbRequireSandbox(b)
	profile := fmt.Sprintf("opb-%d-%d", n, opbProfileSeq.Add(1))
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
	rows := make([]*statedb.InstanceRow, n)
	names := make([]string, n)
	home, _ := os.UserHomeDir()
	for i := range rows {
		id := fmt.Sprintf("opb-%04d", i)
		tool := tools[i%len(tools)]
		status := statuses[i%len(statuses)]
		if socket != "" {
			tool, status = "shell", "idle"
		}
		names[i] = "agentdeck_" + id
		rows[i] = &statedb.InstanceRow{
			ID: id, Title: fmt.Sprintf("bench %04d %s", i, tool), ProjectPath: home,
			GroupPath: fmt.Sprintf("work/team-%d", i%10), Order: i, Tool: tool,
			Status: status, TmuxSession: names[i], TmuxSocketName: socket,
			CreatedAt: time.Now().Add(-time.Hour), ToolData: json.RawMessage(`{}`),
		}
		if socket == "" {
			// No server on this socket: status reads fail fast, like a
			// deck whose sessions are all stopped.
			rows[i].TmuxSocketName = "opb-absent"
		}
	}
	if err := db.SaveInstances(rows); err != nil {
		b.Fatal(err)
	}
	return profile, names
}

// opbLoadHome is the TUI startup path: model, window size, session load from
// state.db, applying the load, first View. It returns the model and the frame.
func opbLoadHome(b *testing.B, profile string, want int) (*Home, string) {
	h := NewHomeWithProfile(profile)
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	loaded, ok := h.loadSessions().(loadSessionsMsg)
	if !ok || loaded.err != nil {
		b.Fatalf("loadSessions: ok=%v err=%v", ok, loaded.err)
	}
	if len(loaded.instances) != want {
		b.Fatalf("loaded %d sessions, want %d", len(loaded.instances), want)
	}
	h.Update(loaded)
	frame := h.View()
	if strings.TrimSpace(frame) == "" {
		b.Fatal("empty first frame")
	}
	return h, frame
}

func opbCloseHome(h *Home) {
	h.cancel()
	if h.storage != nil {
		_ = h.storage.Close()
	}
}

// BenchmarkOp_TUIFirstFrame: NewHome + load N sessions from state.db + first
// populated View (process start and terminal paint not included).
func BenchmarkOp_TUIFirstFrame(b *testing.B) {
	for _, n := range []int{100, 500} {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			profile, _ := opbSeedProfile(b, n, "")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h, _ := opbLoadHome(b, profile, n)
				b.StopTimer()
				opbCloseHome(h)
				b.StartTimer()
			}
		})
	}
}

// BenchmarkOp_TUIListRender: one full View() of the session list with N
// sessions loaded (120x40 terminal).
func BenchmarkOp_TUIListRender(b *testing.B) {
	for _, n := range []int{100, 500} {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			profile, _ := opbSeedProfile(b, n, "")
			h, _ := opbLoadHome(b, profile, n)
			defer opbCloseHome(h)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if h.View() == "" {
					b.Fatal("empty frame")
				}
			}
		})
	}
}

// BenchmarkOp_TUIKeyNav: one cursor key (down, or up on the way back) plus
// the frame it produces, with N sessions.
func BenchmarkOp_TUIKeyNav(b *testing.B) {
	for _, n := range []int{100, 500} {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			profile, _ := opbSeedProfile(b, n, "")
			h, _ := opbLoadHome(b, profile, n)
			defer opbCloseHome(h)
			span := len(h.flatItems) - 1
			if span < 1 {
				span = 1
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				key := tea.KeyDown
				if (i/span)%2 == 1 {
					key = tea.KeyUp
				}
				h.Update(tea.KeyMsg{Type: key})
				_ = h.View()
			}
		})
	}
}

// BenchmarkOp_TUISearch: filtering N loaded sessions by a typed query (the
// work behind each keystroke in the / search).
func BenchmarkOp_TUISearch(b *testing.B) {
	const n = 500
	b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
		profile, _ := opbSeedProfile(b, n, "")
		h, _ := opbLoadHome(b, profile, n)
		defer opbCloseHome(h)
		h.instancesMu.RLock()
		insts := append([]*session.Instance(nil), h.instances...)
		h.instancesMu.RUnlock()
		queries := []string{"b", "be", "bench 01", "team-3", "codex", "zzz"}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = session.FilterByQuery(insts, queries[i%len(queries)])
		}
	})
}

// BenchmarkOp_TUIStatusPass: one TUI status sweep (backgroundStatusUpdate)
// over N live shell sessions on a private tmux socket.
func BenchmarkOp_TUIStatusPass(b *testing.B) {
	if _, err := exec.LookPath("tmux"); err != nil {
		b.Skip("tmux not installed")
	}
	for _, n := range []int{10, 50} {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			opbRequireSandbox(b)
			socket := fmt.Sprintf("opbs%d-%d", os.Getpid(), opbProfileSeq.Add(1))
			b.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
			profile, names := opbSeedProfile(b, n, socket)
			for _, name := range names {
				if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", name, "-x", "120", "-y", "40", "/bin/sh").CombinedOutput(); err != nil {
					b.Fatalf("tmux new-session: %v %s", err, out)
				}
			}
			prev := tmux.DefaultSocketName()
			tmux.SetDefaultSocketName(socket)
			b.Cleanup(func() { tmux.SetDefaultSocketName(prev) })
			h, _ := opbLoadHome(b, profile, n)
			defer opbCloseHome(h)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h.navigationHotUntil.Store(0)
				h.lastFullStatusSweep.Store(0)
				h.backgroundStatusUpdate()
			}
			b.StopTimer()
			if h.lastFullStatusSweep.Load() == 0 {
				b.Fatal("status sweep returned early")
			}
		})
	}
}
