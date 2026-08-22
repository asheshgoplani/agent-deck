package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOMPLifecycle_LaunchSendRestart(t *testing.T) {
	skipIfNoTmuxBinary(t)
	fake, err := filepath.Abs(filepath.Join("testdata", "fake-omp"))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(fake); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("fake-omp must exist and be executable: %v", err)
	}

	t.Setenv("HOME", t.TempDir())
	withConfig(t, &UserConfig{OMP: OMPSettings{Command: fake, DefaultModel: "test/model"}})
	inst := NewInstanceWithTool("omp-lifecycle", t.TempDir(), "omp")
	t.Cleanup(func() { _ = inst.Kill() })

	if err := inst.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	started := waitForPane(t, inst, "OMP started", 15*time.Second)
	if !strings.Contains(started, "model=test/model") || !strings.Contains(started, "instance="+inst.ID) {
		t.Fatalf("launch did not propagate model and instance identity:\n%s", started)
	}
	if err := inst.tmuxSession.SendKeysAndEnter("hello omp"); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForPane(t, inst, "answered: hello omp", 15*time.Second)

	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart(): %v", err)
	}
	waitForPane(t, inst, "OMP resumed", 15*time.Second)
}
