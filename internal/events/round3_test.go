package events

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPublishProfileKeepsTwoProfilesSeparate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	resetDefaultForTest()
	t.Cleanup(resetDefaultForTest)
	SetProfile("alpha")
	t.Cleanup(func() { SetProfile("default") })
	PublishProfile("alpha", "session.transition", "a", nil)
	PublishProfile("beta", "session.transition", "b", nil)
	if err := CloseDefault(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ profile, session string }{{"alpha", "a"}, {"beta", "b"}} {
		dir, err := busDirFor(tc.profile)
		if err != nil {
			t.Fatal(err)
		}
		bus, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := bus.Cursor(); got != 1 {
			t.Errorf("%s cursor = %d, want 1", tc.profile, got)
		}
		_ = bus.Close()
	}
}

func TestCloseDefaultHasDeadlineWithHeldWriterLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	resetDefaultForTest()
	t.Cleanup(resetDefaultForTest)
	PublishDefault("blocked", "", nil)
	// Ensure the tap has reached a bus before holding its disk lock.
	bus := Default()
	if !bus.Flush(5 * time.Second) {
		t.Fatal("initial frame did not flush")
	}
	lock, err := os.OpenFile(filepath.Join(bus.dir, "writer.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	PublishDefault("blocked", "", nil)
	done := make(chan error, 1)
	go func() { done <- CloseDefault() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("CloseDefault blocked on another process's writer lock")
	}
}

func TestOutputBatchThroughput(t *testing.T) {
	bus, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const frames = 2000
	start := time.Now()
	for i := 0; i < frames; i++ {
		bus.Publish("tmux.output", "session", nil)
	}
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
	perSecond := float64(frames) / time.Since(start).Seconds()
	t.Logf("tmux.output: %.0f frames/s", perSecond)
	if perSecond < 20000 {
		t.Fatalf("tap throughput %.0f frames/s below 20000", perSecond)
	}
}
