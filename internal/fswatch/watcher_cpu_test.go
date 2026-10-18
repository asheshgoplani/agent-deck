//go:build unix

package fswatch

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func processCPU(t *testing.T) time.Duration {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Skipf("process CPU time unavailable: %v", err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// A directory that changes before every poll is read in full each time. A
// large one (the shared hooks dir while agents work) must not turn that into
// a share of a core that grows with its size.
func TestPollingBusyLargeDirectoryCPUIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("CPU measurement")
	}
	dir := t.TempDir()
	for i := 0; i < 5000; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("artifact-%05d.json", i)), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	w := newPollingWatcher(10 * time.Millisecond)
	defer w.Close()
	if err := w.Add(dir); err != nil {
		t.Fatal(err)
	}
	go func() {
		for range w.Events {
		}
	}()
	// A hook write (temp file renamed into place) every 20 ms.
	tmp, status := filepath.Join(dir, ".status.json.tmp"), filepath.Join(dir, "status.json")
	const window = 3 * time.Second
	before, start := processCPU(t), time.Now()
	for i := 0; time.Since(start) < window; i++ {
		if err := os.WriteFile(tmp, []byte(fmt.Sprint(i)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, status); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	share := float64(processCPU(t)-before) / float64(time.Since(start))
	t.Logf("polling 5000 entries under churn: %.1f%% of one core", 100*share)
	if share > 0.15 {
		t.Fatalf("polling a churning 5000-entry directory used %.1f%% of one core, want at most 15%%", 100*share)
	}
}
