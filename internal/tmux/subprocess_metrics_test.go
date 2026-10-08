package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSubprocessMetricsActualExecution(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	before := SubprocessStarts()
	cmd := exec.Command(bin)
	if SubprocessStarts() != before {
		t.Fatal("constructing command counted as subprocess")
	}
	if err := commandRun(cmd); err != nil {
		t.Fatal(err)
	}
	if SubprocessStarts() != before+1 {
		t.Fatal("successful start was not counted")
	}
	if err := commandRun(exec.Command(filepath.Join(t.TempDir(), "tmux"))); err == nil {
		t.Fatal("missing executable started")
	}
	if SubprocessStarts() != before+1 {
		t.Fatal("failed launch counted as subprocess")
	}
}

func TestRunUncountedOmitsStatusBudget(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	before := SnapshotStarts()
	totalBefore := SubprocessStarts()
	RunUncounted(func() {
		if err := commandRun(exec.Command(bin)); err != nil {
			t.Fatal(err)
		}
	})
	if SubprocessStarts() != totalBefore+1 {
		t.Fatalf("uncounted start missing from SubprocessStarts: before=%d after=%d", totalBefore, SubprocessStarts())
	}
	if got := before.Charged(); got != 0 {
		t.Fatalf("RunUncounted charged %d calls to the status budget", got)
	}
	if err := commandRun(exec.Command(bin)); err != nil {
		t.Fatal(err)
	}
	if got := before.Charged(); got != 1 {
		t.Fatalf("counted start charged %d, want 1", got)
	}
}

func TestRefreshStatusBarImmediateNotChargedPerViewer(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		"  case \"$arg\" in\n" +
		"    list-clients) printf '0|webpts\\n0|menupts\\n'; exit 0 ;;\n" +
		"  esac\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	prior := DefaultSocketName()
	SetDefaultSocketName("status-bar-budget")
	t.Cleanup(func() { SetDefaultSocketName(prior) })

	before := SnapshotStarts()
	if err := RefreshStatusBarImmediate(); err != nil {
		t.Fatal(err)
	}
	if SubprocessStarts() == before.total {
		t.Fatal("status-bar refresh did not start tmux")
	}
	if got := before.Charged(); got != 0 {
		t.Fatalf("per-client status-bar refresh charged %d calls; web viewers must not count against the session budget", got)
	}
}
