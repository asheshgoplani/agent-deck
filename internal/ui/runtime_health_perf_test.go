//go:build runtimehealthperf

package ui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Runs the production all-session status sweep with 100 fake, disconnected
// sessions and isolated tmux responses. No live terminal or server is required.
func TestPerf_RuntimeHealth(t *testing.T) {
	runRuntimeHealthPerf(t, false)
}

func TestPerf_RuntimeHealthCodex(t *testing.T) {
	runRuntimeHealthPerf(t, true)
}

func runRuntimeHealthPerf(t *testing.T, codex bool) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("AGENTDECK_HOME", filepath.Join(root, "deck"))
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexit 0\n"
	if codex {
		var names, windows, panes strings.Builder
		for i := 0; i < 100; i++ {
			name := fmt.Sprintf("agentdeck_health_%03d", i)
			fmt.Fprintln(&names, name)
			fmt.Fprintf(&windows, "%s|1|0|codex\n", name)
			fmt.Fprintf(&panes, "%s|codex|0|0|0|Codex\n", name)
		}
		t.Setenv("HEALTH_FAKE_NAMES", names.String())
		t.Setenv("HEALTH_FAKE_WINDOWS", windows.String())
		t.Setenv("HEALTH_FAKE_PANES", panes.String())
		t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
		t.Setenv("HEALTH_FAKE_CALLS", filepath.Join(root, "calls"))
		script = `#!/bin/sh
if [ "$1" = -u ]; then shift; fi
if [ "$1" = -L ]; then shift 2; fi
printf '%s\n' "$1" >> "$HEALTH_FAKE_CALLS"
case "$1" in
list-sessions) printf '%s' "$HEALTH_FAKE_NAMES" ;;
list-windows) printf '%s' "$HEALTH_FAKE_WINDOWS" ;;
list-panes) printf '%s' "$HEALTH_FAKE_PANES" ;;
show-environment) printf 'CODEX_SESSION_ID=00000000-0000-4000-8000-000000000001\n' ;;
capture-pane) printf '• Working (1s • esc to interrupt)\n' ;;
*) exit 0 ;;
esac
`
	}
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	priorSocket := tmux.DefaultSocketName()
	tmux.SetDefaultSocketName("runtime-health-perf")
	t.Cleanup(func() { tmux.SetDefaultSocketName(priorSocket) })
	h := &Home{}
	for i := 0; i < 100; i++ {
		inst := &session.Instance{ID: fmt.Sprintf("fake-%03d", i), Title: fmt.Sprintf("Fake %d", i), Tool: "shell", Status: session.StatusRunning}
		if codex {
			inst.Tool = "codex"
			inst.CodexSessionID = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
			inst.SetTmuxSessionForTest(tmux.ReconnectSessionLazy(fmt.Sprintf("agentdeck_health_%03d", i), inst.Title, root, "codex", "active"))
		}
		h.instances = append(h.instances, inst)
	}
	multiplier := 1.0
	if value := os.Getenv("PERF_BUDGET_MULTIPLIER"); value != "" {
		var err error
		multiplier, err = strconv.ParseFloat(value, 64)
		if err != nil || multiplier <= 0 || math.IsInf(multiplier, 0) || math.IsNaN(multiplier) {
			t.Fatalf("invalid PERF_BUDGET_MULTIPLIER %q", value)
		}
	}
	dir := filepath.Join(root, "health")
	stop := health.Start(dir, "perf-test", filepath.Join(root, "hooks"))
	h.backgroundStatusUpdate()
	stop()
	if codex {
		data, err := os.ReadFile(filepath.Join(root, "calls"))
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		for _, line := range strings.Fields(string(data)) {
			counts[line]++
		}
		t.Logf("tmux commands: %v", counts)
	}
	if h.lastFullStatusSweep.Load() == 0 {
		t.Fatal("full status path did not complete")
	}
	for _, inst := range h.instances {
		if !codex && inst.GetStatusThreadSafe() == session.StatusRunning {
			t.Fatalf("%s was not polled: status=%s", inst.ID, inst.GetStatusThreadSafe())
		}
		if codex && (inst.GetToolThreadSafe() != "codex" || (inst.GetStatusThreadSafe() != session.StatusRunning && inst.GetStatusThreadSafe() != session.StatusWaiting)) {
			t.Fatalf("%s missed active Codex metadata path: tool=%s status=%s", inst.ID, inst.GetToolThreadSafe(), inst.GetStatusThreadSafe())
		}
	}
	report, err := health.Report(dir, time.Hour)
	if err != nil || len(report.Processes) != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	sample := report.Processes[0].Latest
	if sample.StatusPassMS == nil || sample.OpenFDs == nil || sample.TmuxCalls == nil || sample.Sessions == nil {
		t.Fatalf("missing real pass measurements: %+v", sample)
	}
	if *sample.Sessions != 100 {
		t.Fatalf("sessions=%d", *sample.Sessions)
	}
	t.Logf("100 fake sessions: codex=%v pass=%.2f ms, fds=%d, tmux starts=%d, multiplier=%g", codex, *sample.StatusPassMS, *sample.OpenFDs, *sample.TmuxCalls, multiplier)
	if *sample.StatusPassMS >= float64(health.StatusPassBudget.Milliseconds())*multiplier {
		t.Errorf("pass %.2f ms exceeds budget", *sample.StatusPassMS)
	}
	if *sample.OpenFDs >= health.DescriptorBudget {
		t.Fatalf("fds=%d exceeds budget", *sample.OpenFDs)
	}
	if *sample.TmuxCalls <= 0 || *sample.TmuxCalls > int64(2**sample.Sessions) {
		t.Errorf("tmux calls=%d outside budget", *sample.TmuxCalls)
	}
}
