//go:build linux

package procowner

// Maintainer process-level check for #2514. Real
// processes, real /proc, real signals, inside a throwaway container whose pid 1
// is tini. A fake `opencode` script is started by a pane leader twice: as the
// 2.x host service (`serve --service`) and as a 1.x per-TUI server
// (`serve --port 4096`); each runs a child (`sleep`). The leader then exits, so
// both trees are reparented to pid 1, which is the shape #2511 item 4 reports.
// The oracle compares only strings so it compiles on origin/main too.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestMaint2514_LiveSharedServiceSurvivesVerifyAndReap(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 300 &\nwait\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	leader := exec.Command("sh", "-c", fmt.Sprintf("%s serve --service & %s serve --port 4096 & sleep 1", bin, bin))
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	p := LinuxProber{}
	r, err := Claim(p, ClaimInput{InstanceID: "maint2514", Generation: 1, PanePID: leader.Process.Pid, Command: "sh"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Attribute(p, r, nil); err != nil {
		t.Fatal(err)
	}
	_ = leader.Wait()
	time.Sleep(200 * time.Millisecond)

	table, _ := commandsForCheck()
	role := map[int]string{}
	for _, m := range r.Members {
		row := table[m.PID]
		role[m.PID] = fmt.Sprint(row.Args)
		t.Logf("member pid=%d ppid=%d argv=%v", m.PID, row.PPID, row.Args)
	}
	t.Cleanup(func() {
		for _, m := range r.Members {
			_ = syscall.Kill(m.PID, syscall.SIGKILL)
		}
	})
	// 2 servers + 2 sleeps, plus the leader's own short `sleep 1`, which is
	// gone by the time Verify runs.
	if len(r.Members) < 4 {
		t.Fatalf("expected at least 4 attributed members (2 servers + 2 sleeps), got %d", len(r.Members))
	}

	report := Verify(p, r)
	t.Logf("verify verdict=%s reason=%q", report.Verdict, report.Reason)
	shared, owned := 0, 0
	for _, s := range report.Members {
		t.Logf("  verify pid=%d state=%s argv=%s", s.Member.PID, s.State, role[s.Member.PID])
		switch string(s.State) {
		case "shared":
			shared++
		case "owned":
			owned++
		}
	}

	rep := Reap(p, OSSignaler{}, r, ReapOptions{TermGrace: time.Second, KillGrace: time.Second})
	t.Logf("reap verdict=%s reason=%q", rep.Verdict, rep.Reason)
	sharedLeft := 0
	for _, o := range rep.Outcomes {
		alive := syscall.Kill(o.Member.PID, 0) == nil
		t.Logf("  reap pid=%d outcome=%s alive_after=%v argv=%s", o.Member.PID, o.Outcome, alive, role[o.Member.PID])
		if o.Outcome == "shared_service" && alive {
			sharedLeft++
		}
	}

	if shared != 2 || owned != 2 {
		t.Errorf("verify: want 2 shared (service + its child) and 2 owned (1.x server + its child), got shared=%d owned=%d", shared, owned)
	}
	if sharedLeft != 2 {
		t.Errorf("reap: want the 2.x service and its child left running, got %d", sharedLeft)
	}
}

// commandsForCheck reads argv/ppid straight from /proc for logging, without
// depending on the PR's Commands() so the file also compiles on main.
func commandsForCheck() (map[int]struct {
	PPID int
	Args []string
}, error) {
	out := map[int]struct {
		PPID int
		Args []string
	}{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		var pid int
		if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		info, err := parseProcStat(stat)
		if err != nil {
			continue
		}
		cmd, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		var args []string
		start := 0
		for i, b := range cmd {
			if b == 0 {
				args = append(args, string(cmd[start:i]))
				start = i + 1
			}
		}
		out[pid] = struct {
			PPID int
			Args []string
		}{info.PPID, args}
	}
	return out, nil
}
