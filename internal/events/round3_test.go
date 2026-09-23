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
	released := false
	defer func() {
		if !released {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		}
	}()
	PublishDefault("blocked", "", nil)
	done := make(chan error, 1)
	go func() { done <- CloseDefault() }()
	select {
	case err := <-done:
		if err != ErrCloseTimeout {
			t.Fatalf("CloseDefault error = %v, want timeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CloseDefault blocked on another process's writer lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	released = true
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("background close did not finish after lock release")
	}
	opened, err := Open(bus.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if got := opened.Stats().Dropped; got == 0 {
		t.Fatal("abandoned frame was not counted")
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
	opened, err := Open(bus.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := opened.Cursor(); got != frames {
		t.Errorf("persisted %d/%d output frames", got, frames)
	}
	if got := opened.Stats().Dropped; got != 0 {
		t.Errorf("output drops = %d", got)
	}
	_ = opened.Close()
	perSecond := float64(frames) / time.Since(start).Seconds()
	t.Logf("tmux.output: %.0f frames/s", perSecond)
	if perSecond < 20000 {
		t.Fatalf("tap throughput %.0f frames/s below 20000", perSecond)
	}
}

func TestDefaultOutputTapThroughput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	resetDefaultForTest()
	t.Cleanup(resetDefaultForTest)
	SetProfile("tap-bench")
	t.Cleanup(func() { SetProfile("default") })
	const frames = 2000
	start := time.Now()
	for i := 0; i < frames; i++ {
		PublishDefault("tmux.output", "session", nil)
	}
	if err := CloseDefault(); err != nil {
		t.Fatal(err)
	}
	perSecond := float64(frames) / time.Since(start).Seconds()
	dir, err := busDirFor("tap-bench")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Cursor(); got != frames {
		t.Errorf("persisted %d/%d tap frames", got, frames)
	}
	if got := b.Stats().Dropped; got != 0 {
		t.Errorf("tap drops = %d", got)
	}
	_ = b.Close()
	t.Logf("tmux.output default tap: %.0f frames/s", perSecond)
	if perSecond < 20000 {
		t.Fatalf("default tap throughput %.0f frames/s below 20000", perSecond)
	}
}
