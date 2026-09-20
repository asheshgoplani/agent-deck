package session

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

// The trigger matrix (docs/recall.md, issue #2329): recall enabled AND
// backfill_on_enable both gate whether the daemon ever starts the
// background pass. maybeStartInitialRecallBackfill must never start it
// otherwise, and must start it at most once per daemon even across repeated
// ticks (the poll loop calls it every SyncOnce).

func TestMaybeStartInitialRecallBackfill_RecallDisabled(t *testing.T) {
	recallHome(t, false)
	d := NewTransitionDaemon()
	d.maybeStartInitialRecallBackfill(context.Background())
	if d.recallBackfillStarted {
		t.Fatal("started with [recall] enabled = false")
	}
}

func TestMaybeStartInitialRecallBackfill_BackfillOnEnableDisabled(t *testing.T) {
	recallHome(t, true)
	cfg := userConfigCache
	f := false
	cfg.Recall.BackfillOnEnable = &f
	withConfig(t, cfg)

	d := NewTransitionDaemon()
	d.maybeStartInitialRecallBackfill(context.Background())
	if d.recallBackfillStarted {
		t.Fatal("started with [recall] backfill_on_enable = false")
	}
}

func TestMaybeStartInitialRecallBackfill_StartsOnlyOnce(t *testing.T) {
	recallHome(t, true) // backfill_on_enable defaults true

	d := NewTransitionDaemon()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.maybeStartInitialRecallBackfill(ctx)
	if !d.recallBackfillStarted {
		t.Fatal("did not start with recall enabled and backfill_on_enable at its true default")
	}

	// A later tick under a config that would otherwise refuse must still be
	// a no-op: the in-process guard, not a config re-check, decides.
	cfg := userConfigCache
	off := false
	cfg.Recall.Enabled = &off
	withConfig(t, cfg)
	d.maybeStartInitialRecallBackfill(ctx)
	if !d.recallBackfillStarted {
		t.Fatal("second call reset the started guard")
	}
}

// TestInitialRecallBackfill_EndToEnd drives the whole daemon-triggered path
// for real (recall test harness): an isolated HOME with a genuine Claude
// transcript corpus on disk, a live TransitionDaemon, its real background
// goroutine, and the real recall.db it writes to, exactly as
// notify-daemon's poll loop would trigger it (issue #2329).
func TestInitialRecallBackfill_EndToEnd(t *testing.T) {
	home := recallHome(t, true)
	// recallHome also lays out sample Codex/pi/Gemini/OpenCode/Hermes
	// fixtures for other recall tests to share; scope this daemon pass to
	// Claude so it indexes only the corpus this test itself wrote (matching
	// RecallRoots(), which reads [recall] harnesses).
	cfg := userConfigCache
	cfg.Recall.Harnesses = []string{"claude"}
	withConfig(t, cfg)

	stats, err := testcorpus.Generate(filepath.Join(home, ".claude"), testcorpus.Options{Files: 5, Seed: 11, SubagentEvery: 2})
	if err != nil {
		t.Fatal(err)
	}

	dbPath, err := recall.DBPath()
	if err != nil {
		t.Fatal(err)
	}
	if st, err := store.OpenCurrent(dbPath); err == nil {
		status, serr := st.InitialBackfillStatus()
		st.Close()
		if serr == nil && status.State != store.InitialBackfillPending {
			t.Fatalf("precondition: initial_backfill state = %q before the daemon ran, want pending", status.State)
		}
	}

	d := NewTransitionDaemon()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.maybeStartInitialRecallBackfill(ctx)
	if !d.recallBackfillStarted {
		t.Fatal("trigger did not fire with recall enabled and a Claude corpus on disk")
	}

	deadline := time.Now().Add(15 * time.Second)
	var status store.InitialBackfillStatus
	for {
		st, err := store.OpenCurrent(dbPath)
		if err == nil {
			status, err = st.InitialBackfillStatus()
			st.Close()
			if err == nil && status.State == store.InitialBackfillDone {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("initial backfill did not finish in time (last state %+v)", status)
		}
		time.Sleep(25 * time.Millisecond)
	}

	st, err := store.OpenCurrent(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var sessions int
	if err := st.W.QueryRow(`SELECT count(*) FROM session`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != stats.Files {
		t.Fatalf("sessions = %d, want %d (every generated transcript indexed, nothing else: [recall] harnesses is scoped to claude)", sessions, stats.Files)
	}
	if status.SessionsDone != stats.Files {
		t.Fatalf("status.sessions_done = %d, want %d", status.SessionsDone, stats.Files)
	}
	if status.SessionsPending != 0 {
		t.Fatalf("status.sessions_pending = %d, want 0", status.SessionsPending)
	}
}
