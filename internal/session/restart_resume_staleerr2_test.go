package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Field bug STALEERR2 (1.16.32-rc.5, 2026-10-20 22:54 CEST): an idle build
// refresh restarted a child cleanly and the parent got "Restarted, but the
// resumed session reports an error." A daemon pass read the light as error
// from the old process's SessionEnd; before emitTurn judged that flip, the
// resumed process wrote its SessionStart over the SessionEnd in the hook file,
// so judgeRestart found no old exit and called the flip the new process's
// error. The SessionEnd is still in the hook event history.

// writeHookHistory writes child id's hook event history as the hook handler
// does: one {"at","event"} line per event.
func writeHookHistory(t *testing.T, id string, events map[string]time.Time) {
	t.Helper()
	var b strings.Builder
	for _, ev := range []string{"SessionEnd", "SessionStart"} {
		if at, ok := events[ev]; ok {
			fmt.Fprintf(&b, "{\"at\":%q,\"event\":%q,\"status\":\"waiting\"}\n", at.UTC().Format(time.RFC3339Nano), ev)
		}
	}
	path := filepath.Join(filepath.Dir(hookStatusFilePath(id)), id+".events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStaleErr2_OldExitOverwrittenByNewSessionStartIsHeld(t *testing.T) {
	type order struct {
		base time.Duration // the respawn's offset into its second
		// end is when the old Claude wrote its SessionEnd after the respawn;
		// histLag how much later the history line read the clock.
		end, histLag time.Duration
		// hookFirst: the first judgement still reads the SessionEnd in the
		// hook file, the second (same pass) reads the new SessionStart.
		hookFirst bool
	}
	cases := map[string]order{
		"both judged after SessionStart/x.900": {base: 900 * time.Millisecond, end: 130 * time.Millisecond},
		"both judged after SessionStart/x.100": {base: 100 * time.Millisecond, end: 130 * time.Millisecond},
		"hook file first/x.100":                {base: 100 * time.Millisecond, end: 130 * time.Millisecond, hookFirst: true},
		// The SessionEnd lands late in the second after the spawn record:
		// held from the hook file, so from the history too.
		"hook file first/late SessionEnd": {base: 100 * time.Millisecond, end: 1400 * time.Millisecond, hookFirst: true},
		// The history's clock read crosses a second the hook file's did not.
		"hook file first/history a second later": {base: 900 * time.Millisecond, end: 50 * time.Millisecond, histLag: 60 * time.Millisecond, hookFirst: true},
		// Lander r2: a SessionEnd late in the hook file's window whose history
		// line crosses into the second past it.
		"hook file first/late SessionEnd, history past the window":                {base: 100 * time.Millisecond, end: 1850 * time.Millisecond, histLag: 60 * time.Millisecond, hookFirst: true},
		"both judged after SessionStart/late SessionEnd, history past the window": {base: 100 * time.Millisecond, end: 1850 * time.Millisecond, histLag: 60 * time.Millisecond},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			n := f.finishedTurn(t)
			sends := *f.sends
			respawn := time.Now().Add(-4 * time.Second).Truncate(time.Second).Add(c.base)
			st.realRestart(f, respawn, 20*time.Millisecond, c.end)
			end, start := respawn.Add(c.end+c.histLag), respawn.Add(c.end+870*time.Millisecond)
			writeHookHistory(t, f.child.ID, map[string]time.Time{"SessionEnd": end, "SessionStart": start})
			// The resumed process's SessionStart replaces the old SessionEnd
			// after the pass read the light.
			sessionStart := &HookStatus{Status: "waiting", Event: "SessionStart", UpdatedAt: start.Truncate(time.Second)}
			if !c.hookFirst {
				st.hook = sessionStart
			}

			// Judged twice in one pass (recordTerminalTurns, the snapshot loop).
			f.d.emitTurn("default", f.child, f.byID, "waiting", "error", start.Add(450*time.Millisecond), true)
			st.hook = sessionStart
			f.d.emitTurn("default", f.child, f.byID, "waiting", "error", start.Add(460*time.Millisecond), true)
			for _, r := range f.inboxRecords(t)[n:] {
				if r.IsUrgent() || strings.Contains(r.Text, "reports an error") {
					t.Fatalf("false urgent on a clean idle restart: %q", r.Text)
				}
			}
			if *f.sends != sends {
				t.Fatalf("no wake, sends %d -> %d: %+v", sends, *f.sends, f.inboxRecords(t)[n:])
			}
			// The next pass reads waiting: the one info line.
			f.d.emitTurn("default", f.child, f.byID, "error", "waiting", start.Add(2*time.Second), true)
			added := f.inboxRecords(t)[n:]
			if len(added) != 1 || added[0].IsUrgent() || !strings.Contains(added[0].Text, "conversation resumed") {
				t.Fatalf("want exactly the one info line, got %+v", added)
			}
		})
	}
}

// Only the old process's exit is held: with the new SessionStart on file, an
// error whose history holds no SessionEnd of this restart (none, one from
// before the token, one past the hook file's window) is still the new
// process's, urgent.
func TestStaleErr2_NoOldExitInHistoryIsNews(t *testing.T) {
	cases := map[string]func(respawn time.Time) map[string]time.Time{
		"no history":              func(time.Time) map[string]time.Time { return nil },
		"SessionEnd before token": func(r time.Time) map[string]time.Time { return map[string]time.Time{"SessionEnd": r.Add(-time.Minute)} },
		"SessionEnd seconds after spawn": func(r time.Time) map[string]time.Time {
			return map[string]time.Time{"SessionEnd": r.Add(2500 * time.Millisecond)}
		},
	}
	for name, history := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			f.finishedTurn(t)
			sends := *f.sends
			respawn := time.Now().Add(-3 * time.Second).Truncate(time.Second).Add(100 * time.Millisecond)
			st.realRestart(f, respawn, 20*time.Millisecond, 130*time.Millisecond)
			if h := history(respawn); h != nil {
				writeHookHistory(t, f.child.ID, h)
			}
			st.hook = &HookStatus{Status: "waiting", Event: "SessionStart", UpdatedAt: respawn.Add(time.Second).Truncate(time.Second)}
			f.d.emitTurn("default", f.child, f.byID, "waiting", "error", respawn.Add(2*time.Second), true)
			errs := f.recordsTo(t, "error")
			if len(errs) != 1 || !errs[0].IsUrgent() || !strings.Contains(errs[0].Text, "reports an error") || *f.sends == sends {
				t.Fatalf("an error with no old exit on record is news: %+v", f.inboxRecords(t))
			}
		})
	}
}

// A hold from the history is still only the old exit: a resumed process that
// is dead pages "exited", and a history SessionEnd two seconds past the one
// held from the hook file is another exit, not the same one read later.
func TestStaleErr2_HistoryHoldKeepsNewProcessErrors(t *testing.T) {
	t.Run("new process dead", func(t *testing.T) {
		f := newTurnTestFixture(t)
		st := stubRestartPIDs(t)
		f.finishedTurn(t)
		respawn := time.Now().Add(-4 * time.Second).Truncate(time.Second).Add(100 * time.Millisecond)
		st.realRestart(f, respawn, 20*time.Millisecond, 130*time.Millisecond)
		writeHookHistory(t, f.child.ID, map[string]time.Time{"SessionEnd": respawn.Add(130 * time.Millisecond), "SessionStart": respawn.Add(time.Second)})
		st.hook = &HookStatus{Status: "waiting", Event: "SessionStart", UpdatedAt: respawn.Add(time.Second).Truncate(time.Second)}
		st.alive[st.pane] = false
		f.d.emitTurn("default", f.child, f.byID, "waiting", "error", respawn.Add(2*time.Second), true)
		errs := f.recordsTo(t, "error")
		if len(errs) != 1 || !errs[0].IsUrgent() || !strings.Contains(errs[0].Text, "exited") {
			t.Fatalf("a dead resumed process pages: %+v", f.inboxRecords(t))
		}
	})
	t.Run("history two seconds past the held stamp", func(t *testing.T) {
		f := newTurnTestFixture(t)
		st := stubRestartPIDs(t)
		f.finishedTurn(t)
		sends := *f.sends
		respawn := time.Now().Add(-4 * time.Second).Truncate(time.Second).Add(900 * time.Millisecond)
		st.realRestart(f, respawn, 20*time.Millisecond, 50*time.Millisecond) // hook file: second x
		f.d.emitTurn("default", f.child, f.byID, "waiting", "error", respawn.Add(time.Second), true)
		if errs := f.recordsTo(t, "error"); len(errs) != 0 {
			t.Fatalf("precondition: the old exit is held: %+v", errs)
		}
		end := respawn.Add(1150 * time.Millisecond) // second x+2, still in the history window
		writeHookHistory(t, f.child.ID, map[string]time.Time{"SessionEnd": end, "SessionStart": end.Add(100 * time.Millisecond)})
		st.hook = &HookStatus{Status: "waiting", Event: "SessionStart", UpdatedAt: end.Add(100 * time.Millisecond).Truncate(time.Second)}
		f.d.emitTurn("default", f.child, f.byID, "waiting", "error", respawn.Add(2*time.Second), true)
		errs := f.recordsTo(t, "error")
		if len(errs) != 1 || !errs[0].IsUrgent() || !strings.Contains(errs[0].Text, "reports an error") || *f.sends == sends {
			t.Fatalf("another SessionEnd two seconds later is news: %+v", f.inboxRecords(t))
		}
	})
}

// judgeRestart reads the history inside emitTurn: a FIFO planted at its path
// must not block the daemon.
func TestStaleErr2_HistoryFIFODoesNotBlock(t *testing.T) {
	newTurnTestFixture(t)
	id := "fifo-child"
	path := filepath.Join(filepath.Dir(hookStatusFilePath(id)), id+".events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan []restartHookEvent, 1)
	go func() { done <- restartHookHistory(id) }()
	select {
	case evs := <-done:
		if len(evs) != 0 {
			t.Fatalf("a FIFO is no history: %+v", evs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restartHookHistory blocked on a FIFO")
	}
}
