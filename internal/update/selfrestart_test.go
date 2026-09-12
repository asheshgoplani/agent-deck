package update

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseVersionOutput(t *testing.T) {
	cases := map[string]string{
		"Agent Deck v1.16.1":                             "1.16.1",
		"Agent Deck v1.16.1 (update available: v1.16.2)": "1.16.1",
		"Agent Deck v1.17.0-rc.1":                        "1.17.0-rc.1",
		"garbage":                                        "",
		"":                                               "",
		"warning: something\nAgent Deck v1.16.3\nmore text": "1.16.3",
	}
	for in, want := range cases {
		if got := ParseVersionOutput(in); got != want {
			t.Errorf("ParseVersionOutput(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCheckExecutable pins the pre-exec sanity check that keeps a
// half-written or wrong target from tearing the process down for nothing.
func TestCheckExecutable(t *testing.T) {
	dir := t.TempDir()
	if err := CheckExecutable(""); err == nil {
		t.Fatal("empty path must fail")
	}
	if err := CheckExecutable(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file must fail")
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckExecutable(empty); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file: err = %v", err)
	}
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckExecutable(plain); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("non-executable file: err = %v", err)
	}
	if err := CheckExecutable(dir); err == nil {
		t.Fatal("directory must fail")
	}
	ok := filepath.Join(dir, "ok")
	if err := os.WriteFile(ok, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckExecutable(ok); err != nil {
		t.Fatalf("executable file: unexpected %v", err)
	}
	// ExecSelf must refuse before exec'ing anything for a bad target.
	if err := ExecSelf(plain, nil); err == nil {
		t.Fatal("ExecSelf with a non-executable target must fail")
	}
}

type fakeWatch struct {
	fp        Fingerprint
	statErr   error
	version   string
	probeErr  error
	probes    int
	idle      bool
	idleAsks  int
	restarts  []string
	restartEr error
}

func (f *fakeWatch) watcher() *Watcher {
	w := &Watcher{
		Exe:            "/bin/agent-deck",
		RunningVersion: "1.16.0",
		Interval:       time.Hour,
		Stat: func(string) (Fingerprint, error) {
			return f.fp, f.statErr
		},
		Probe: func(string) (string, error) {
			f.probes++
			return f.version, f.probeErr
		},
		Idle: func() bool {
			f.idleAsks++
			return f.idle
		},
		Restart: func(exe string) error {
			f.restarts = append(f.restarts, exe)
			return f.restartEr
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	w.fillDefaults()
	w.probed = f.fp
	return w
}

func fpAt(sec, size int64) Fingerprint {
	return Fingerprint{ModTime: time.Unix(sec, 0), Size: size}
}

// TestWatcher_RestartsOnceIdle walks the headless path: unchanged file
// never probes, a newer file probes once, a busy process is asked again
// each tick without restarting, and the first idle tick restarts.
func TestWatcher_RestartsOnceIdle(t *testing.T) {
	f := &fakeWatch{fp: fpAt(1, 1), version: "1.16.1"}
	w := f.watcher()
	for i := 0; i < 3; i++ {
		if w.tick() {
			t.Fatal("unchanged file must not restart")
		}
	}
	if f.probes != 0 || f.idleAsks != 0 {
		t.Fatalf("unchanged file: probes=%d idleAsks=%d, want 0/0", f.probes, f.idleAsks)
	}
	f.fp = fpAt(2, 2)
	if w.tick() {
		t.Fatal("busy process must not restart")
	}
	if w.tick() {
		t.Fatal("busy process must not restart on the next tick either")
	}
	if f.probes != 1 || f.idleAsks != 2 || len(f.restarts) != 0 {
		t.Fatalf("busy: probes=%d idleAsks=%d restarts=%v", f.probes, f.idleAsks, f.restarts)
	}
	f.idle = true
	if !w.tick() {
		t.Fatal("idle process with a newer build must restart")
	}
	if len(f.restarts) != 1 || f.restarts[0] != "/bin/agent-deck" {
		t.Fatalf("restarts = %v", f.restarts)
	}
}

// TestWatcher_OlderOrSameVersionIsIgnored pins that a rebuilt file with
// the same or an older version never triggers a restart.
func TestWatcher_OlderOrSameVersionIsIgnored(t *testing.T) {
	for _, v := range []string{"1.16.0", "1.15.9"} {
		f := &fakeWatch{fp: fpAt(1, 1), version: v, idle: true}
		w := f.watcher()
		f.fp = fpAt(2, 2)
		if w.tick() || len(f.restarts) != 0 || f.idleAsks != 0 {
			t.Fatalf("version %s: restarts=%v idleAsks=%d", v, f.restarts, f.idleAsks)
		}
	}
}

// TestWatcher_ProbeFailureRetriesThenGivesUp pins the retry budget for a
// half-written file and that a later fingerprint change re-arms it.
func TestWatcher_ProbeFailureRetriesThenGivesUp(t *testing.T) {
	f := &fakeWatch{fp: fpAt(1, 1), probeErr: errors.New("exec format error"), idle: true}
	w := f.watcher()
	f.fp = fpAt(2, 2)
	for i := 0; i < watcherMaxProbeFailures+2; i++ {
		if w.tick() {
			t.Fatal("failed probe must not restart")
		}
	}
	if f.probes != watcherMaxProbeFailures {
		t.Fatalf("probes = %d, want %d", f.probes, watcherMaxProbeFailures)
	}
	f.fp, f.probeErr, f.version = fpAt(3, 3), nil, "1.16.2"
	if !w.tick() {
		t.Fatal("finished install must restart")
	}
}

// TestWatcher_RestartFailureDisarmsUntilNextChange pins that a failed exec
// is logged once and not retried for the same file.
func TestWatcher_RestartFailureDisarmsUntilNextChange(t *testing.T) {
	f := &fakeWatch{fp: fpAt(1, 1), version: "1.16.1", idle: true, restartEr: errors.New("exec failed")}
	w := f.watcher()
	f.fp = fpAt(2, 2)
	if w.tick() || w.tick() {
		t.Fatal("failed restart must report not restarted")
	}
	if len(f.restarts) != 1 {
		t.Fatalf("restarts = %d, want exactly 1 attempt", len(f.restarts))
	}
	f.fp, f.version, f.restartEr = fpAt(3, 3), "1.16.2", nil
	if !w.tick() || len(f.restarts) != 2 {
		t.Fatalf("new file must re-arm: restarts=%d", len(f.restarts))
	}
}

// TestWatcher_RunStopsAfterRestart drives Run with a short interval and a
// stat that flips to a newer file, and checks it returns once the restart
// hook succeeded (the "stop serving" contract for the remote agent).
func TestWatcher_RunStopsAfterRestart(t *testing.T) {
	f := &fakeWatch{fp: fpAt(1, 1), version: "1.16.1", idle: true}
	w := f.watcher()
	w.Interval = 5 * time.Millisecond
	ticks := 0
	w.Stat = func(string) (Fingerprint, error) {
		ticks++
		if ticks > 2 {
			return fpAt(2, 2), nil
		}
		return fpAt(1, 1), nil
	}
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		w.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("Run did not return after the restart hook succeeded")
	}
	if len(f.restarts) != 1 {
		t.Fatalf("restarts = %v", f.restarts)
	}
}

// TestWatcher_EmptyExeIsInert pins that a process without a resolvable
// executable path never polls.
func TestWatcher_EmptyExeIsInert(t *testing.T) {
	w := &Watcher{Interval: time.Millisecond, Stat: func(string) (Fingerprint, error) {
		t.Fatal("must not stat without a path")
		return Fingerprint{}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	w.Run(ctx)
}
