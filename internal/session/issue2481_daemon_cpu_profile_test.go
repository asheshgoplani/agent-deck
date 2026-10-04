package session

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

// TestIssue2481_ProfileDaemonTicks profiles N notify-daemon passes over a
// fleet shaped like the measured host: 51 profiles (2 populated), 81
// sessions, 886 Claude project directories, real-size transcripts, a live TUI
// heartbeat, and three Claude sessions whose transcript is not at the exact
// path. Opt-in: runs only with -test.bench set; writes cpu-2481.pprof in the
// package directory.
func TestIssue2481_ProfileDaemonTicks(t *testing.T) {
	if f := flag.Lookup("test.bench"); f == nil || f.Value.String() == "" {
		t.Skip("profiling: run with -bench=NONE")
	}
	d := newDaemonFleetFixture(t)
	d.SyncOnce(context.Background()) // first pass seeds the baseline

	const ticks = 100
	out, err := os.Create("cpu-2481.pprof")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := pprof.StartCPUProfile(out); err != nil {
		t.Fatal(err)
	}
	ns := measureTicks(d, ticks)
	pprof.StopCPUProfile()
	t.Logf("MEASURE daemon_tick ticks=%d wall_ns/tick=%d", ticks, ns)
}

func measureTicks(d *TransitionDaemon, ticks int) int64 {
	t0 := time.Now()
	for i := 0; i < ticks; i++ {
		d.SyncOnce(context.Background())
	}
	return time.Since(t0).Nanoseconds() / int64(ticks)
}

// newDaemonFleetFixture builds the fleet under a throwaway HOME and returns a
// daemon over it.
func newDaemonFleetFixture(t testing.TB) *TransitionDaemon {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(func() { ClearUserConfigCache(); ResetInboxFingerprintCacheForTest() })
	if err := os.MkdirAll(filepath.Join(home, ".agent-deck"), 0o700); err != nil {
		t.Fatal(err)
	}

	projects := filepath.Join(home, ".claude", "projects")
	for i := 0; i < 886; i++ {
		if err := os.MkdirAll(filepath.Join(projects, fmt.Sprintf("-Users-x-proj-%03d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bigTurn := fxAssistantText("a-big", strings.Repeat("lorem ipsum ", 400))

	for p := 0; p < 49; p++ {
		st, err := NewStorageWithProfile(fmt.Sprintf("empty-%02d", p))
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	populate := func(profile, conductorID string, n int) {
		st, err := NewStorageWithProfile(profile)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		now := time.Now()
		var all []*Instance
		conductor := &Instance{ID: conductorID, Title: "conductor-" + profile, ProjectPath: filepath.Join(home, "c-"+profile),
			GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusWaiting, CreatedAt: now,
			ClaudeSessionID: fmt.Sprintf("00000000-0000-0000-0000-%012d", len(profile))}
		all = append(all, conductor)
		for i := 0; i < n; i++ {
			inst := &Instance{
				ID:              fmt.Sprintf("%s-child-%02d", profile, i),
				Title:           fmt.Sprintf("worker-%02d", i),
				ProjectPath:     filepath.Join(home, "work", profile, fmt.Sprintf("p%02d", i)),
				GroupPath:       DefaultGroupPath,
				ParentSessionID: conductorID,
				Tool:            "claude",
				Status:          StatusWaiting,
				CreatedAt:       now,
				ClaudeSessionID: fmt.Sprintf("%08d-1111-2222-3333-%012d", i, len(profile)),
			}
			if i%5 == 4 {
				inst.Tool = "shell"
				inst.ClaudeSessionID = ""
			}
			all = append(all, inst)
		}
		for idx, inst := range all {
			if inst.ClaudeSessionID == "" {
				continue
			}
			_ = os.MkdirAll(inst.ProjectPath, 0o755)
			if idx%25 == 7 {
				continue // transcript not at the exact path: resolution falls back to a glob
			}
			dir := filepath.Join(projects, ConvertToClaudeDirName(inst.ProjectPath))
			_ = os.MkdirAll(dir, 0o755)
			lines := []string{fxHuman("u0", "go"), fxAssistantText("a0", "done.")}
			reps := 40
			if idx%10 == 0 {
				reps = 2000 // a few multi-MB transcripts
			}
			for r := 0; r < reps; r++ {
				lines = append(lines, bigTurn)
			}
			if err := os.WriteFile(filepath.Join(dir, inst.ClaudeSessionID+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.SaveWithGroups(all, nil); err != nil {
			t.Fatal(err)
		}
		db := st.GetDB()
		if err := db.RegisterInstance(false); err != nil { // a live TUI owns the statuses
			t.Fatal(err)
		}
		for _, inst := range all {
			_ = db.WriteStatus(inst.ID, string(inst.Status), inst.Tool)
		}
	}
	populate("personal", "cond-personal", 62)
	populate("agent-deck", "cond-agentdeck", 17)

	d := NewTransitionDaemon()
	d.turnLiveCheck = func(*Instance) bool { return true }
	t.Cleanup(func() {
		for _, s := range d.storages {
			s.Close()
		}
	})
	return d
}
