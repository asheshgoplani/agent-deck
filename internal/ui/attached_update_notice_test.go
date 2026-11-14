package ui

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestAttachedUpdate_NoticeWhileAttachedNoRestart pins (a): while attached
// with a newer build on disk the user is told to detach to restart, the
// notice is repeated only now and then, and nothing restarts or touches
// the attach. auto_restart=false (the off switch) shows no notice, since a
// detach would not restart.
func TestAttachedUpdate_NoticeWhileAttachedNoRestart(t *testing.T) {
	h := newAutoRestartTestHome(t)
	var mu sync.Mutex
	var notices []string
	clear := tmux.SetAttachNoticeSink(func(text string) {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, text)
	})
	defer clear()

	h.isAttaching.Store(true)
	// The last tick before the loop parks: the attach blocks the restart.
	if cmd := h.maybeAutoRestart(); cmd != nil || h.restartRequested {
		t.Fatal("no restart may start while attached")
	}
	t0 := time.Now()
	runAttachWorker(t, h, t0, t0.Add(time.Minute), t0.Add(attachUpdateNoticeEvery+time.Minute))

	mu.Lock()
	got := append([]string(nil), notices...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("notices = %q, want one at once and one repeat", got)
	}
	if !strings.Contains(got[0], "v1.16.1") || !strings.Contains(got[0], "detach to restart") {
		t.Fatalf("notice = %q, want the installed version and \"detach to restart\"", got[0])
	}
	if h.restartRequested || h.isQuitting {
		t.Fatalf("attached: requested=%v quitting=%v, want no restart", h.restartRequested, h.isQuitting)
	}

	mu.Lock()
	notices = nil
	mu.Unlock()
	h = newAutoRestartTestHome(t)
	stubUpdateSettings(t, session.UpdateSettings{AutoRestart: boolPtr(false)})
	h.isAttaching.Store(true)
	_ = h.maybeAutoRestart()
	runAttachWorker(t, h, t0)
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 0 {
		t.Fatalf("auto_restart=false: notices = %q, want none", notices)
	}
}

// captureAttachNotices collects what the attach worker shows.
func captureAttachNotices(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var notices []string
	t.Cleanup(tmux.SetAttachNoticeSink(func(text string) {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, text)
	}))
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), notices...)
	}
}

// TestAttachedUpdate_NoNoticeWhenDetachWouldNotRestart: a build held back
// after a failed dry run, or a still running updater, keeps the detach
// from restarting, so the attach is not told to detach.
func TestAttachedUpdate_NoNoticeWhenDetachWouldNotRestart(t *testing.T) {
	for name, block := range map[string]func(h *Home){
		"held": func(h *Home) {
			h.autoRestartHoldUntil, h.autoRestartHoldReason = time.Now().Add(time.Hour), "new binary failed its dry run (x)"
		},
		"updating": func(h *Home) { h.autoInstallInFlight = "1.16.2" },
	} {
		t.Run(name, func(t *testing.T) {
			notices := captureAttachNotices(t)
			h := newAutoRestartTestHome(t)
			stubStatBinary(t, fpAt(2, 2), nil) // the held build, unchanged
			prev := probeBinaryVersion
			probeBinaryVersion = func(string) (string, error) { t.Error("unchanged file probed"); return "", nil }
			t.Cleanup(func() { probeBinaryVersion = prev })
			block(h)
			h.isAttaching.Store(true)
			_ = h.maybeAutoRestart()
			runAttachWorker(t, h, time.Now())
			if got := notices(); len(got) != 0 {
				t.Fatalf("notices = %q, want none: a detach would not restart", got)
			}
		})
	}
}

// TestAttachedUpdate_ProbeRetriedWhileAttached: a version probe of a new
// file that fails (half-written file, timeout) is retried on the next
// worker tick, like the loop's, instead of hiding the update for the rest
// of the attach.
func TestAttachedUpdate_ProbeRetriedWhileAttached(t *testing.T) {
	notices := captureAttachNotices(t)
	stubUpdateSettings(t, session.UpdateSettings{})
	h := newRestartTestHome(t)
	h.isAttaching.Store(true)
	_ = h.maybeAutoRestart() // nothing newer yet

	stubStatBinary(t, fpAt(2, 2), nil)
	var probes int
	prev := probeBinaryVersion
	probeBinaryVersion = func(string) (string, error) {
		probes++
		if probes == 1 {
			return "", errors.New("timed out")
		}
		return "1.16.1", nil
	}
	t.Cleanup(func() { probeBinaryVersion = prev })
	t0 := time.Now()
	runAttachWorker(t, h, t0, t0.Add(5*time.Second))
	got := notices()
	if len(got) != 1 || !strings.Contains(got[0], "v1.16.1") {
		t.Fatalf("notices = %q after %d probes, want the retried probe's notice", got, probes)
	}
}
