package session

import (
	"strings"
	"testing"
	"time"
)

// Issue #2813 with #2806: the unseen-boot info line spends the restart token
// early, so what the token's sweep watched for (the resumed process dying, an
// error banner) must still reach the parent once, and only once. Each pass
// runs the two in syncProfile's order: confirmIdleExits, then
// sweepRestartTokens. Adapted from the lander's review probes.

type restartWatchPass struct {
	f    *turnTestFixture
	prev map[string]string
}

func (p *restartWatchPass) run(status string, now time.Time) {
	st := map[string]string{p.f.child.ID: status, p.f.parent.ID: "waiting"}
	p.f.d.confirmIdleExits("default", p.f.byID, p.prev, st, true, now)
	p.f.d.sweepRestartTokens("default", p.f.byID, st, now)
	p.prev = st
}

func restartWatchUrgent(t *testing.T, f *turnTestFixture, n int) []TransitionNotificationEvent {
	t.Helper()
	var out []TransitionNotificationEvent
	for _, r := range f.inboxRecords(t)[n:] {
		if r.IsUrgent() {
			out = append(out, r)
		}
	}
	return out
}

func restartWatchInfoLines(t *testing.T, f *turnTestFixture, n int) int {
	t.Helper()
	lines := 0
	for _, r := range f.inboxRecords(t)[n:] {
		if strings.Contains(r.Text, "conversation resumed") {
			lines++
		}
	}
	return lines
}

// restartWatchSetup restarts the child (the old SessionEnd stamped at the
// spawn second) with a 2 m boot timeout and the real 5 s death grace.
// history says whether the hook event history proves the new SessionStart.
func restartWatchSetup(t *testing.T, history bool) (*turnTestFixture, *pidStub, *bool, time.Time, int) {
	t.Helper()
	t.Setenv("AGENTDECK_RESTART_BOOT_TIMEOUT", "2m")
	t.Setenv("AGENTDECK_IDLE_EXIT_GRACE", "")
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	n := f.finishedTurn(t)
	paneAlive := true
	f.d.idleExitAlive = func(*Instance) bool { return paneAlive }
	old := restartSessionStartSince
	restartSessionStartSince = func(string, time.Time) bool { return history }
	t.Cleanup(func() { restartSessionStartSince = old })
	at := time.Now()
	st.restart(f, at)
	f.appendTurn(t, fxResumeMeta())
	return f, st, &paneAlive, at, n
}

// The old SessionEnd lands last, so the light reads error; the info line goes
// out at +6 s and spends the token. The resumed process then dies inside the
// hook's freshness window: no waiting -> error flip, no token. The parent's
// last word must not stay "conversation resumed".
func TestIssue2813_LateEndCrashAfterInfoLineIsReported(t *testing.T) {
	f, st, paneAlive, at, n := restartWatchSetup(t, true)
	p := &restartWatchPass{f: f, prev: map[string]string{f.child.ID: "waiting"}}
	p.run("error", at.Add(2*time.Second))
	p.run("error", at.Add(6*time.Second))
	if got := restartWatchInfoLines(t, f, n); got != 1 {
		t.Fatalf("precondition: one info line at +6s, got %d: %+v", got, f.inboxRecords(t)[n:])
	}
	if u := restartWatchUrgent(t, f, n); len(u) != 0 {
		t.Fatalf("no urgent while the resumed process lives, got %+v", u)
	}

	st.alive[st.pane] = false
	*paneAlive = false
	for i := 0; i < 5; i++ {
		p.run("error", at.Add(time.Duration(20+3*i)*time.Second))
	}
	p.run("error", at.Add(3*time.Minute))
	u := restartWatchUrgent(t, f, n)
	if len(u) != 1 || !strings.Contains(u[0].Text, "exited while idle") {
		t.Fatalf("the resumed process died after the info line: want one urgent death record, got %d: %+v", len(u), u)
	}
}

// Controls: the same death is reported exactly once when the info line went
// out with the light at waiting (#2806's flip), and when no info line went
// out (the token's own sweep).
func TestIssue2813_CrashAfterResumeIsReportedOnce(t *testing.T) {
	t.Run("end first, light waiting", func(t *testing.T) {
		f, st, paneAlive, at, n := restartWatchSetup(t, true)
		st.newHook("waiting", "SessionStart", at)
		p := &restartWatchPass{f: f, prev: map[string]string{f.child.ID: "waiting"}}
		p.run("waiting", at.Add(6*time.Second))
		st.alive[st.pane] = false
		*paneAlive = false
		for i := 0; i < 5; i++ {
			p.run("error", at.Add(time.Duration(20+3*i)*time.Second))
		}
		p.run("error", at.Add(3*time.Minute))
		if u := restartWatchUrgent(t, f, n); len(u) != 1 {
			t.Fatalf("want exactly one urgent, got %d: %+v", len(u), u)
		}
	})
	t.Run("no proof, no info line", func(t *testing.T) {
		f, st, paneAlive, at, n := restartWatchSetup(t, false)
		p := &restartWatchPass{f: f, prev: map[string]string{f.child.ID: "waiting"}}
		p.run("error", at.Add(2*time.Second))
		p.run("error", at.Add(6*time.Second))
		st.alive[st.pane] = false
		*paneAlive = false
		for i := 0; i < 5; i++ {
			p.run("error", at.Add(time.Duration(20+3*i)*time.Second))
		}
		p.run("error", at.Add(3*time.Minute))
		p.run("error", at.Add(3*time.Minute+3*time.Second))
		if u := restartWatchUrgent(t, f, n); len(u) != 1 {
			t.Fatalf("want exactly one urgent, got %d: %+v", len(u), u)
		}
	})
}

// The new process is dead at the boot timeout while the light still reads
// waiting: the sweep reports "did not come back". The light flips to error a
// pass later with the pane gone; #2806 must not report the same death again.
func TestIssue2813_TimeoutThenDeathIsOneUrgent(t *testing.T) {
	f, st, paneAlive, at, n := restartWatchSetup(t, false)
	st.hook = &HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: at.Add(-time.Hour)}
	p := &restartWatchPass{f: f, prev: map[string]string{f.child.ID: "waiting"}}
	p.run("waiting", at.Add(6*time.Second))
	st.alive[st.pane] = false
	*paneAlive = false
	p.run("waiting", at.Add(3*time.Minute)) // sweep: did not come back
	for i := 1; i <= 8; i++ {
		p.run("error", at.Add(3*time.Minute+time.Duration(2*i)*time.Second)) // well past the death grace
	}
	u := restartWatchUrgent(t, f, n)
	if len(u) != 1 || !strings.Contains(u[0].Text, "did not come back") {
		t.Fatalf("one death, one urgent: got %d: %+v", len(u), u)
	}
}

// The info line spends the token at +6 s; an error banner on the resumed
// session is still reported, once, until the boot timeout (it was while the
// token lived), and the info line is not sent again. A banner after the boot
// timeout is not the restart's to report.
func TestIssue2813_BannerAfterInfoLineIsReportedUntilTheBootTimeout(t *testing.T) {
	for _, c := range []struct {
		name   string
		banner time.Duration
		want   int
	}{
		{"banner at +30s", 30 * time.Second, 1},
		{"banner after the boot timeout", 2*time.Minute + 10*time.Second, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, st, paneAlive, at, n := restartWatchSetup(t, true)
			p := &restartWatchPass{f: f, prev: map[string]string{f.child.ID: "waiting"}}
			p.run("error", at.Add(2*time.Second))
			p.run("error", at.Add(6*time.Second)) // info line, token spent
			p.run("error", at.Add(9*time.Second))
			st.substate = "auth-401"
			p.run("error", at.Add(c.banner))
			p.run("error", at.Add(c.banner+3*time.Second))
			u := restartWatchUrgent(t, f, n)
			if len(u) != c.want {
				t.Fatalf("want %d urgent banner record, got %d: %+v", c.want, len(u), u)
			}
			if c.want == 1 && !strings.Contains(u[0].Text, "shows a login or connection error") {
				t.Fatalf("the record says what the pane shows, got %q", u[0].Text)
			}
			if got := restartWatchInfoLines(t, f, n); got != 1 {
				t.Fatalf("the info line is sent once, got %d", got)
			}
			// The broken session then dies: the parent already has its urgent.
			st.alive[st.pane] = false
			*paneAlive = false
			for i := 1; i <= 5; i++ {
				p.run("error", at.Add(c.banner+time.Duration(3+3*i)*time.Second))
			}
			if u := restartWatchUrgent(t, f, n); c.want == 1 && len(u) != 1 {
				t.Fatalf("a death after the banner record is the same failure: want 1 urgent, got %d: %+v", len(u), u)
			}
		})
	}
}

// Round-2 review: restart #1 resumes into an auth banner, whose urgent record
// marks the child told. Restart #2 (self-heal or the user) resumes it with the
// old SessionEnd landing last again, so the light never leaves error and the
// told mark is never cleared by a live pane out of error. Restart #2's info
// line hands the child to the death watch; when the re-resumed process dies,
// the parent must hear it, not keep "conversation resumed" as the last word.
func TestIssue2813_ChainedRestartAfterBannerDeathIsReported(t *testing.T) {
	f, st, paneAlive, at, n := restartWatchSetup(t, true)
	p := &restartWatchPass{f: f, prev: map[string]string{f.child.ID: "waiting"}}
	p.run("error", at.Add(2*time.Second))
	p.run("error", at.Add(6*time.Second)) // info line #1
	st.substate = "auth-401"
	p.run("error", at.Add(30*time.Second)) // banner urgent, child told
	if u := restartWatchUrgent(t, f, n); len(u) != 1 {
		t.Fatalf("precondition: one banner urgent, got %d: %+v", len(u), u)
	}
	// Restart #2 at +60 s; the new process shows no banner.
	at2 := at.Add(60 * time.Second)
	st.restart(f, at2)
	st.substate = ""
	p.run("error", at2.Add(2*time.Second))
	p.run("error", at2.Add(6*time.Second)) // info line #2
	if got := restartWatchInfoLines(t, f, n); got != 2 {
		t.Fatalf("want an info line per restart, got %d", got)
	}
	st.alive[st.pane] = false
	*paneAlive = false
	for s := 20; s <= 40; s += 3 {
		p.run("error", at2.Add(time.Duration(s)*time.Second))
	}
	p.run("error", at2.Add(4*time.Minute))
	u := restartWatchUrgent(t, f, n)
	if len(u) != 2 {
		t.Fatalf("the re-resumed process died after info line #2: want 2 urgent (banner, death), got %d: %+v", len(u), u)
	}
	if !strings.Contains(u[1].Text, "exited while idle") {
		t.Fatalf("the second urgent reports the death, got %q", u[1].Text)
	}
}

// Round-3 review: "did not come back" with the pane gone marks the child told.
// A retry restart then writes a token but fails before it spawns anything and
// clears the token again. That token names no new process, so it must not
// drop the told mark: the later waiting -> error flip is the same death.
func TestIssue2813_FailedRetryAfterTimeoutKeepsOneUrgent(t *testing.T) {
	f, st, paneAlive, at, n := restartWatchSetup(t, false)
	st.hook = &HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: at.Add(-time.Hour)}
	p := &restartWatchPass{f: f, prev: map[string]string{f.child.ID: "waiting"}}
	p.run("waiting", at.Add(6*time.Second))
	st.alive[st.pane] = false
	*paneAlive = false
	p.run("waiting", at.Add(3*time.Minute)) // sweep: did not come back, told
	// The retry writes its token (no new pid yet), a daemon pass sees it,
	// then i.restart() fails and the token is cleared.
	at2 := at.Add(3*time.Minute + time.Second)
	noteRestartToken(f.child, at2)
	p.run("waiting", at2.Add(time.Second))
	clearRestartToken(f.child.ID)
	for i := 1; i <= 8; i++ {
		p.run("error", at2.Add(time.Duration(2+2*i)*time.Second))
	}
	u := restartWatchUrgent(t, f, n)
	if len(u) != 1 || !strings.Contains(u[0].Text, "did not come back") {
		t.Fatalf("one death, one urgent (a retry that never spawned is no new process): got %d: %+v", len(u), u)
	}
}

// Round-3 review (optional): the banner caught on the token's first check
// (light waiting, no info line yet) is the failure's urgent record; when that
// broken session then dies, the waiting -> error flip is the same failure.
func TestIssue2813_FirstCheckBannerThenDeathIsOneUrgent(t *testing.T) {
	f, st, paneAlive, at, n := restartWatchSetup(t, true)
	st.newHook("waiting", "SessionStart", at)
	st.substate = "auth-401"
	p := &restartWatchPass{f: f, prev: map[string]string{f.child.ID: "waiting"}}
	p.run("waiting", at.Add(2*time.Second)) // banner on the token's first check
	st.alive[st.pane] = false
	*paneAlive = false
	for s := 5; s <= 40; s += 3 {
		p.run("error", at.Add(time.Duration(s)*time.Second))
	}
	u := restartWatchUrgent(t, f, n)
	if len(u) != 1 || !strings.Contains(u[0].Text, "shows a login or connection error") {
		t.Fatalf("banner then death is one failure: want 1 urgent (the banner), got %d: %+v", len(u), u)
	}
}
