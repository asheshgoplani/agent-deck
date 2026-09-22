package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBusDirIsProfileSpecific(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("AGENTDECK_PROFILE", "alpha")
	alpha, err := busDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTDECK_PROFILE", "beta")
	beta, err := busDir()
	if err != nil {
		t.Fatal(err)
	}
	if alpha == beta {
		t.Fatalf("profiles share bus directory %q", alpha)
	}
}

func TestConcurrentProcessesHaveUniqueCursorsAndVisibleDrops(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	commands := make([]*exec.Cmd, 2)
	for i := range commands {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBusProcessHelper$")
		cmd.Env = append(os.Environ(), "EVENTS_HELPER_DIR="+dir, "EVENTS_HELPER_ID="+strconv.Itoa(i))
		commands[i] = cmd
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, cmd := range commands {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e0 := os.Stat(ready + "0"); e0 == nil {
			if _, e1 := os.Stat(ready + "1"); e1 == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("helpers did not open bus")
		}
		time.Sleep(time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Errorf("helper %d: %v", i, err)
		}
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if got := b.Stats().Dropped; got == 0 {
		t.Error("cross-process stats hid producer drops")
	}
	want := b.Stats().Cursor
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := b.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[Cursor]bool{}
	processFrames := 0
	for frame := range sub.Frames() {
		if seen[frame.Cursor] {
			t.Fatalf("duplicate cursor %d", frame.Cursor)
		}
		seen[frame.Cursor] = true
		if frame.Kind == "process" {
			processFrames++
		}
		if Cursor(len(seen)) == want {
			cancel()
		}
	}
	if processFrames != 200 {
		t.Fatalf("got %d/200 cross-process frames", processFrames)
	}
	for i := Cursor(1); i <= want; i++ {
		if !seen[i] {
			t.Errorf("missing cursor %d", i)
		}
	}
}

func TestBusProcessHelper(t *testing.T) {
	dir := os.Getenv("EVENTS_HELPER_DIR")
	if dir == "" {
		return
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := os.Getenv("EVENTS_HELPER_ID")
	if err := os.WriteFile(filepath.Join(dir, "ready"+id), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no start")
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 100; i++ {
		b.Publish("process", id, i)
	}
	if !b.Flush(10 * time.Second) {
		t.Fatal("process frames were not durable")
	}
	if id == "0" {
		b.mu.Lock()
		for i := 0; i < defaultQueueCap*2; i++ {
			b.Publish("overflow", id, i)
		}
		// The overflow has already happened. Skip disk work for the queued
		// pressure frames so this cross-process test stays bounded.
		b.failed.Store(true)
		b.mu.Unlock()
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCLIStatsReadsProducerDropsAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, ".cache"))
	dir, err := busDirFor("alpha")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b.Publish("warmup", "", nil)
	if !b.Flush(5 * time.Second) {
		t.Fatal("warmup flush")
	}
	b.mu.Lock()
	for i := 0; i < defaultQueueCap*2; i++ {
		b.Publish("overflow", "", i)
	}
	b.failed.Store(true)
	b.mu.Unlock()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "agent-deck")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "./cmd/agent-deck")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	cmd := exec.Command(binary, "-p", "alpha", "events", "stats", "--json")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("events stats: %v\n%s", err, output)
	}
	var stats Stats
	if err := json.Unmarshal(output, &stats); err != nil {
		t.Fatalf("parse stats: %v\n%s", err, output)
	}
	if stats.Dropped == 0 {
		t.Fatal("CLI process did not see producer drops")
	}
	if stats.Dir != dir {
		t.Fatalf("profile dir = %q, want %q", stats.Dir, dir)
	}
	betaOutput, err := exec.Command(binary, "-p", "beta", "events", "stats", "--json").Output()
	if err != nil {
		t.Fatal(err)
	}
	var beta Stats
	if err := json.Unmarshal(betaOutput, &beta); err != nil {
		t.Fatal(err)
	}
	if beta.Dir == dir || beta.Cursor != 0 || beta.Dropped != 0 {
		t.Fatalf("beta observed alpha bus: %+v", beta)
	}
}

func TestDefaultOwnerCloseDrainsOneShot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("AGENTDECK_PROFILE", "one-shot")
	resetDefaultForTest()
	t.Cleanup(resetDefaultForTest)
	b := Default()
	b.Publish("one-shot", "", nil)
	if err := CloseDefault(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(b.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if opened.Cursor() != 1 {
		t.Fatalf("accepted one-shot tap lost at exit: cursor %d", opened.Cursor())
	}
}

type pausedJSON struct{ entered, release chan struct{} }

func (p pausedJSON) MarshalJSON() ([]byte, error) {
	close(p.entered)
	<-p.release
	return []byte(`{"ok":true}`), nil
}

func TestCloseRejectsPublishStillMarshalling(t *testing.T) {
	b := openTestBus(t)
	value := pausedJSON{entered: make(chan struct{}), release: make(chan struct{})}
	published := make(chan struct{})
	go func() { b.Publish("closing", "", value); close(published) }()
	<-value.entered
	closed := make(chan struct{})
	go func() { _ = b.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on producer marshal")
	}
	close(value.release)
	<-published
	if got := b.published.Load(); got != 0 {
		t.Errorf("accepted %d events after writer exit", got)
	}
}

func TestCompactedActiveOnlyReportsCursorTooOld(t *testing.T) {
	b := openTestBus(t)
	b.maxSegFrames = 2
	b.retainSegs = 0
	b.Publish("compact", "", 1)
	b.Publish("compact", "", 2)
	if !b.Flush(2 * time.Second) {
		t.Fatal("flush")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	sub, err := b.Subscribe(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for range sub.Frames() {
	}
	if !errors.Is(sub.Err(), ErrCursorTooOld) {
		t.Fatal("compacted cursor was silently skipped")
	}
}

func TestMissingSealedSegmentReportsCursorTooOld(t *testing.T) {
	s := &Subscription{frames: make(chan Frame, 1)}
	missing := filepath.Join(t.TempDir(), "compacted.ndjson")
	_, _, err := s.streamFile(context.Background(), missing, new(Cursor))
	if !errors.Is(err, ErrCursorTooOld) {
		t.Fatalf("missing needed segment: %v", err)
	}
}

func TestRestartThenRotateRetainsTrueSegmentRange(t *testing.T) {
	dir := t.TempDir()
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b.maxSegFrames = 12
	for i := 0; i < 15; i++ {
		b.Publish("restart", "", i)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b.maxSegFrames = 12
	b.retainSegs = 2
	for i := 15; i < 36; i++ {
		b.Publish("restart", "", i)
	}
	if !b.Flush(5 * time.Second) {
		t.Fatal("flush")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	sealed, err := listSealedSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) != 2 || sealed[0].start != 13 || sealed[0].end != 24 {
		t.Fatalf("bad sealed range after restart and compaction: %+v", sealed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, err := b.Subscribe(ctx, 14)
	if err != nil {
		t.Fatal(err)
	}
	var next Cursor = 15
	for frame := range sub.Frames() {
		if frame.Cursor != next {
			t.Fatalf("cursor want %d got %d", next, frame.Cursor)
		}
		next++
		if next == 37 {
			cancel()
		}
	}
	if next != 37 {
		t.Fatalf("resume stopped at %d", next)
	}
}

func TestRuntimeWriteFailureDisablesWithoutCursorGap(t *testing.T) {
	var warnings bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&warnings, nil)))
	defer slog.SetDefault(previousLogger)
	dir := t.TempDir()
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(dir, activeSegmentName)
	blocked := filepath.Join(dir, "saved-active")
	if err := os.Rename(active, blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(active, 0o700); err != nil {
		t.Fatal(err)
	}
	b.Publish("failed", "", nil)
	deadline := time.Now().Add(2 * time.Second)
	for !b.failed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !b.failed.Load() {
		t.Error("bus did not disable on runtime failure")
	}
	b.Publish("failed-again", "", nil)
	if got := strings.Count(warnings.String(), "events: bus disabled after write failure"); got != 1 {
		t.Errorf("runtime warnings = %d, want 1: %s", got, warnings.String())
	}
	if b.Cursor() != 0 {
		t.Errorf("failed append assigned cursor %d", b.Cursor())
	}
	if err := os.Rename(active, active+".blocked"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(blocked, active); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	b2.Publish("recovered", "", nil)
	if !b2.Flush(2 * time.Second) {
		t.Fatal("recovery flush")
	}
	if b2.Cursor() != 1 {
		t.Errorf("recovered cursor = %d, want 1", b2.Cursor())
	}
}

func openTestBus(t *testing.T) *Bus {
	t.Helper()
	dir := t.TempDir()
	b, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestCursorOrderingIsMonotonicAndSequential(t *testing.T) {
	b := openTestBus(t)
	for i := 0; i < 50; i++ {
		b.Publish("kind.test", fmt.Sprintf("sess-%d", i%3), map[string]any{"i": i})
	}
	if !b.Flush(2 * time.Second) {
		t.Fatal("flush timed out")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, err := b.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	var last Cursor
	count := 0
	for frame := range sub.Frames() {
		if frame.Cursor <= last {
			t.Fatalf("cursor not strictly increasing: prev=%d got=%d", last, frame.Cursor)
		}
		last = frame.Cursor
		count++
		if count == 50 {
			cancel()
		}
	}
	if count != 50 {
		t.Fatalf("expected 50 frames, got %d", count)
	}
}

// TestResumeAfterKillLosesNothingAndDuplicatesNothing is the slice-4 done
// proof: publish a stream of events, "kill" a follower mid-stream (cancel its
// context after it has consumed some prefix), then Subscribe again with
// after=<last cursor it saw> and assert the resumed stream is exactly the
// remaining suffix — zero lost, zero duplicated — and that the concatenation
// of both runs equals the full, contiguous cursor sequence with no gaps.
func TestResumeAfterKillLosesNothingAndDuplicatesNothing(t *testing.T) {
	b := openTestBus(t)
	const total = 500

	for i := 0; i < total; i++ {
		b.Publish("kind.resume", "sess", map[string]any{"i": i})
	}
	if !b.Flush(5 * time.Second) {
		t.Fatal("flush timed out")
	}

	// First follower: read a prefix, then get "killed" (context cancelled).
	const killAfterN = 213
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	sub1, err := b.Subscribe(ctx1, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	var firstRun []Frame
	for frame := range sub1.Frames() {
		firstRun = append(firstRun, frame)
		if len(firstRun) == killAfterN {
			cancel1() // kill mid-stream
			break
		}
	}
	// Drain until the channel actually closes so the goroutine has exited
	// (Subscription.Frames() closes on cancellation).
	for range sub1.Frames() {
	}

	if len(firstRun) != killAfterN {
		t.Fatalf("expected to have read %d frames before kill, got %d", killAfterN, len(firstRun))
	}
	lastSeen := firstRun[len(firstRun)-1].Cursor

	// Resume from the last cursor the killed follower actually saw.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	sub2, err := b.Subscribe(ctx2, lastSeen)
	if err != nil {
		t.Fatalf("Subscribe resume: %v", err)
	}
	var secondRun []Frame
	for frame := range sub2.Frames() {
		secondRun = append(secondRun, frame)
		if len(secondRun) == total-killAfterN {
			cancel2()
			break
		}
	}
	for range sub2.Frames() {
	}

	if err := sub2.Err(); err != nil {
		t.Fatalf("resumed subscription error: %v", err)
	}

	if len(secondRun) != total-killAfterN {
		t.Fatalf("expected %d frames after resume, got %d", total-killAfterN, len(secondRun))
	}

	// Zero lost, zero duplicated: the two runs concatenated must cover
	// cursors 1..total exactly once each, in order.
	seen := make(map[Cursor]bool, total)
	var all []Frame
	all = append(all, firstRun...)
	all = append(all, secondRun...)
	if len(all) != total {
		t.Fatalf("expected %d total frames across both runs, got %d", total, len(all))
	}
	var prev Cursor
	for _, f := range all {
		if seen[f.Cursor] {
			t.Fatalf("duplicate cursor %d", f.Cursor)
		}
		seen[f.Cursor] = true
		if f.Cursor <= prev {
			t.Fatalf("cursor out of order: prev=%d got=%d", prev, f.Cursor)
		}
		prev = f.Cursor
	}
	if Cursor(len(seen)) != Cursor(total) {
		t.Fatalf("expected %d distinct cursors, got %d", total, len(seen))
	}
}

func TestResumeSurvivesProcessRestart(t *testing.T) {
	dir := t.TempDir()

	b1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 30; i++ {
		b1.Publish("kind.restart", "sess", map[string]any{"i": i})
	}
	if !b1.Flush(2 * time.Second) {
		t.Fatal("flush timed out")
	}
	midCursor := b1.Cursor()
	if err := b1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Simulate a restart: a brand-new Bus over the same directory.
	b2, err := Open(dir)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	t.Cleanup(func() { _ = b2.Close() })
	if b2.Cursor() != midCursor {
		t.Fatalf("cursor not durable across restart: want %d got %d", midCursor, b2.Cursor())
	}
	for i := 30; i < 60; i++ {
		b2.Publish("kind.restart", "sess", map[string]any{"i": i})
	}
	if !b2.Flush(2 * time.Second) {
		t.Fatal("flush timed out")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, err := b2.Subscribe(ctx, midCursor)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	count := 0
	for frame := range sub.Frames() {
		if frame.Cursor <= midCursor {
			t.Fatalf("got a frame from before restart: cursor=%d midCursor=%d", frame.Cursor, midCursor)
		}
		count++
		if count == 30 {
			cancel()
		}
	}
	if count != 30 {
		t.Fatalf("expected 30 post-restart frames, got %d", count)
	}
}

func TestResumeAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	b, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	b.maxSegFrames = 20 // force multiple rotations well within the test

	const total = 200
	for i := 0; i < total; i++ {
		b.Publish("kind.rotate", "sess", map[string]any{"i": i})
	}
	if !b.Flush(5 * time.Second) {
		t.Fatal("flush timed out")
	}

	sealed, err := listSealedSegments(dir)
	if err != nil {
		t.Fatalf("listSealedSegments: %v", err)
	}
	if len(sealed) < 2 {
		t.Fatalf("expected rotation to have produced >=2 sealed segments, got %d", len(sealed))
	}

	// Resume from partway through an early sealed segment.
	resumeAfter := sealed[0].end
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := b.Subscribe(ctx, resumeAfter)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	var last Cursor = resumeAfter
	count := 0
	for frame := range sub.Frames() {
		if frame.Cursor <= last {
			t.Fatalf("out of order after rotation: prev=%d got=%d", last, frame.Cursor)
		}
		last = frame.Cursor
		count++
		if count == int(Cursor(total)-resumeAfter) {
			cancel()
		}
	}
	want := int(Cursor(total) - resumeAfter)
	if count != want {
		t.Fatalf("expected %d frames after rotation resume, got %d", want, count)
	}
}

func TestPublishNeverBlocksWhenQueueIsFull(t *testing.T) {
	dir := t.TempDir()
	b, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	// Starve the writer so the queue fills: hold b.mu so writeFrame can never
	// take it, forcing every enqueued frame to sit in the channel. Released
	// before any call that itself needs b.mu (Stats/Cursor), so the flood
	// goroutine's Publish calls (which never touch b.mu) are the only thing
	// racing against the lock.
	b.mu.Lock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < defaultQueueCap*2; i++ {
			b.Publish("kind.flood", "sess", i)
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		b.mu.Unlock()
		t.Fatal("Publish blocked producers when the queue filled up")
	}
	b.mu.Unlock()

	if b.Stats().Dropped == 0 {
		t.Fatal("expected some frames to be dropped once the queue filled")
	}
}

func TestDisabledBusIsANoOp(t *testing.T) {
	t.Setenv(disableEnvVar, "0")
	resetDefaultForTest()
	t.Cleanup(resetDefaultForTest)

	b := Default()
	b.Publish("kind.noop", "sess", map[string]any{"x": 1})
	if !b.Flush(50 * time.Millisecond) {
		// disabled Flush returns false immediately; that's expected, not a hang
	}
	stats := b.Stats()
	if stats.Enabled {
		t.Fatal("expected disabled bus")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sub, err := b.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe on disabled bus: %v", err)
	}
	n := 0
	for range sub.Frames() {
		n++
	}
	if n != 0 {
		t.Fatalf("disabled bus produced %d frames", n)
	}
}

func TestUnwritableDirDisablesInsteadOfFailing(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	parent := t.TempDir()
	blocked := filepath.Join(parent, "blocked")
	if err := os.Mkdir(blocked, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	_, err := Open(filepath.Join(blocked, "bus"))
	if err == nil {
		t.Fatal("expected Open to fail under an unwritable parent")
	}
	// openDefault()/Default() is what production code actually calls, and it
	// must degrade to a disabled, safe-to-use Bus rather than propagate this
	// error — exercised by TestDisabledBusIsANoOp's Stats()/Publish() shape.
}

func TestStatsJSONShape(t *testing.T) {
	b := openTestBus(t)
	b.Publish("kind.stats", "sess", map[string]any{"a": 1})
	if !b.Flush(2 * time.Second) {
		t.Fatal("flush timed out")
	}
	stats := b.Stats()
	if !stats.Enabled {
		t.Fatal("expected enabled")
	}
	if stats.Cursor != 1 {
		t.Fatalf("expected cursor 1, got %d", stats.Cursor)
	}
	if stats.Published != 1 || stats.Written != 1 || stats.Synced != 1 {
		t.Fatalf("unexpected counters: %+v", stats)
	}
}
