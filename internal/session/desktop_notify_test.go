package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/desknotify"
)

// recordingBackend captures notifications instead of raising a real banner.
type recordingBackend struct {
	titles []string
	bodies []string
}

func (r *recordingBackend) Name() string    { return "recording" }
func (r *recordingBackend) Available() bool { return true }
func (r *recordingBackend) Notify(_ context.Context, title, body string) error {
	r.titles = append(r.titles, title)
	r.bodies = append(r.bodies, body)
	return nil
}

// writeDesktopNotifyConfig points HOME at a temp dir holding a config.toml
// with the given [notifications] body, and clears the config cache both ways.
func writeDesktopNotifyConfig(t *testing.T, body string) {
	t.Helper()
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", originalHome)
		ClearUserConfigCache()
	})

	dir := filepath.Join(tempDir, ".agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "[notifications]\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()
}

// Desktop notifications must be OFF unless asked for: this is the only
// agent-deck signal that interrupts the operator outside the TUI.
func TestDesktopNotifications_DefaultOff(t *testing.T) {
	writeDesktopNotifyConfig(t, "enabled = true")

	if GetNotificationsSettings().GetDesktopEnabled() {
		t.Error("desktop notifications are enabled with no explicit desktop key; must default to off")
	}
}

func TestDesktopNotifications_EnabledFromConfig(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")

	if !GetNotificationsSettings().GetDesktopEnabled() {
		t.Error("desktop = true in config did not enable desktop notifications")
	}
}

// notifyDesktop must be silent when the feature is off, even for a status that
// would otherwise notify.
func TestNotifyDesktop_SuppressedWhenDisabled(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = false")

	rec := &recordingBackend{}
	d := &TransitionDaemon{deskNotifier: desknotify.NewWithBackends(rec)}
	d.notifyDesktop("default", &Instance{ID: "a", Title: "flow"}, string(StatusWaiting))
	d.waitDesktopNotifications()

	if len(rec.titles) != 0 {
		t.Errorf("delivered %d notifications with desktop disabled, want 0", len(rec.titles))
	}
}

func TestNotifyDesktop_DeliversWaitingWhenEnabled(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")

	rec := &recordingBackend{}
	d := &TransitionDaemon{deskNotifier: desknotify.NewWithBackends(rec)}
	d.notifyDesktop("default", &Instance{ID: "a", Title: "flow"}, string(StatusWaiting))
	d.waitDesktopNotifications()

	if len(rec.titles) != 1 {
		t.Fatalf("delivered %d notifications, want 1", len(rec.titles))
	}
	if rec.titles[0] != "flow" {
		t.Errorf("title = %q, want the session title %q so the operator knows which agent", rec.titles[0], "flow")
	}
	if rec.bodies[0] != "needs input" {
		t.Errorf("body = %q, want %q", rec.bodies[0], "needs input")
	}
}

// A session with the per-session opt-out set must stay silent, so one noisy
// session can be muted without disabling notifications globally. This mirrors
// the gate the parent-routing path already honours.
func TestNotifyDesktop_HonorsPerSessionOptOut(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")

	rec := &recordingBackend{}
	d := &TransitionDaemon{deskNotifier: desknotify.NewWithBackends(rec)}
	d.notifyDesktop("default", &Instance{
		ID: "a", Title: "flow", NoTransitionNotify: true,
	}, string(StatusWaiting))
	d.waitDesktopNotifications()

	if len(rec.titles) != 0 {
		t.Errorf("delivered %d notifications for a session with NoTransitionNotify, want 0", len(rec.titles))
	}
}

// Idle is a resting state for a long-lived interactive agent, not an event.
func TestNotifyDesktop_IgnoresIdle(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")

	rec := &recordingBackend{}
	d := &TransitionDaemon{deskNotifier: desknotify.NewWithBackends(rec)}
	d.notifyDesktop("default", &Instance{ID: "a", Title: "flow"}, string(StatusIdle))
	d.waitDesktopNotifications()

	if len(rec.titles) != 0 {
		t.Errorf("delivered %d notifications for idle, want 0", len(rec.titles))
	}
}

// THE SPAM GUARD. terminalHookTransitionCandidate accepts any hook file
// updated within hookFreshWindow (45s), so the hook-candidate call site
// re-derives a candidate for a session that STAYS waiting on every poll. The
// transition notifier's own 90s dedup is downstream of notifyDesktop and
// cannot cover it. Without an edge check this is ~15-22 banners per prompt at
// a 2-3s poll interval, which would make the feature unusable.
func TestNotifyDesktop_OnlyOncePerStatusEntry(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")

	rec := &recordingBackend{}
	d := &TransitionDaemon{
		deskNotifier:      desknotify.NewWithBackends(rec),
		lastDesktopNotify: map[string]string{},
	}
	inst := &Instance{ID: "a", Title: "flow"}

	// Ten poll passes with the session parked in waiting.
	for i := 0; i < 10; i++ {
		d.notifyDesktop("default", inst, string(StatusWaiting))
		d.waitDesktopNotifications()
	}

	if len(rec.titles) != 1 {
		t.Fatalf("delivered %d notifications for one sustained waiting status, want 1: "+
			"a session sitting in waiting must not re-alert on every poll", len(rec.titles))
	}
}

// Escalation must still alert: a session that goes waiting then error has hit
// a genuinely new condition the operator has not seen.
func TestNotifyDesktop_StatusChangeStillAlerts(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")

	rec := &recordingBackend{}
	d := &TransitionDaemon{
		deskNotifier:      desknotify.NewWithBackends(rec),
		lastDesktopNotify: map[string]string{},
	}
	inst := &Instance{ID: "a", Title: "flow"}

	d.notifyDesktop("default", inst, string(StatusWaiting))
	d.waitDesktopNotifications()
	d.notifyDesktop("default", inst, string(StatusWaiting))
	d.waitDesktopNotifications()
	d.notifyDesktop("default", inst, string(StatusError))
	d.waitDesktopNotifications()

	if len(rec.bodies) != 2 {
		t.Fatalf("delivered %d notifications, want 2 (waiting then error)", len(rec.bodies))
	}
	if rec.bodies[1] != "hit an error" {
		t.Errorf("second body = %q, want %q", rec.bodies[1], "hit an error")
	}
}

// Two different sessions must not suppress each other: the edge record is
// keyed per session, not global.
func TestNotifyDesktop_PerSessionNotGlobal(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")

	rec := &recordingBackend{}
	d := &TransitionDaemon{
		deskNotifier:      desknotify.NewWithBackends(rec),
		lastDesktopNotify: map[string]string{},
	}

	d.notifyDesktop("default", &Instance{ID: "a", Title: "flow"}, string(StatusWaiting))
	d.waitDesktopNotifications()
	d.notifyDesktop("default", &Instance{ID: "b", Title: "natera"}, string(StatusWaiting))
	d.waitDesktopNotifications()

	if len(rec.titles) != 2 {
		t.Fatalf("delivered %d notifications for two distinct sessions, want 2", len(rec.titles))
	}
}

// The same title in two profiles is two different sessions.
func TestNotifyDesktop_PerProfile(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")

	rec := &recordingBackend{}
	d := &TransitionDaemon{
		deskNotifier:      desknotify.NewWithBackends(rec),
		lastDesktopNotify: map[string]string{},
	}
	inst := &Instance{ID: "a", Title: "flow"}

	d.notifyDesktop("default", inst, string(StatusWaiting))
	d.waitDesktopNotifications()
	d.notifyDesktop("work", inst, string(StatusWaiting))
	d.waitDesktopNotifications()

	if len(rec.titles) != 2 {
		t.Fatalf("delivered %d notifications across two profiles, want 2", len(rec.titles))
	}
}

// The edge record must CLEAR when a session goes back to running, or the first
// prompt in a session is the only one that ever alerts, which is worse than the
// spam the edge record prevents. This is the counterpart to
// TestNotifyDesktop_OnlyOncePerStatusEntry: one asserts suppression while
// parked, this asserts release once work resumes.
func TestNotifyDesktop_ReAlertsAfterSessionResumes(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")

	rec := &recordingBackend{}
	d := &TransitionDaemon{
		deskNotifier:      desknotify.NewWithBackends(rec),
		lastDesktopNotify: map[string]string{},
	}
	inst := &Instance{ID: "a", Title: "flow"}

	// First prompt, answered, second prompt: two alerts.
	d.notifyDesktop("default", inst, string(StatusWaiting))
	d.waitDesktopNotifications()
	d.clearDesktopEdgeIfRunning("default", inst.ID, string(StatusRunning))
	d.notifyDesktop("default", inst, string(StatusWaiting))
	d.waitDesktopNotifications()

	if len(rec.titles) != 2 {
		t.Fatalf("delivered %d notifications, want 2: a session that resumed and then "+
			"asked again must alert a second time", len(rec.titles))
	}
}

// Only a return to running releases the edge. Any other status must leave it
// intact, or the suppression it provides evaporates.
// The attention statuses must NOT release the edge: holding it while a session
// sits in waiting or error is what prevents per-poll spam.
func TestClearDesktopEdge_AttentionStatusesDoNotRelease(t *testing.T) {
	d := &TransitionDaemon{lastDesktopNotify: map[string]string{"default|a": "waiting"}}

	for _, s := range []string{"waiting", "error", "stopped", ""} {
		d.clearDesktopEdgeIfRunning("default", "a", s)
		if got := d.lastDesktopNotify["default|a"]; got != "waiting" {
			t.Fatalf("status %q released the edge record (now %q); it must be held so a "+
				"session parked in an attention status does not re-alert every poll", s, got)
		}
	}
}

// Every status meaning "this turn is over" must release the edge. Re-arming on
// running ALONE is insufficient: the hook-candidate dispatch site exists for
// turns too fast for a running snapshot to be observed, so waiting -> idle ->
// waiting can happen with running never seen. Keyed on running only, the second
// genuine prompt is silently suppressed.
func TestClearDesktopEdge_ProgressStatusesRelease(t *testing.T) {
	for _, s := range []string{
		string(StatusRunning), string(StatusIdle), string(StatusStarting),
	} {
		d := &TransitionDaemon{lastDesktopNotify: map[string]string{"default|a": "waiting"}}
		d.clearDesktopEdgeIfRunning("default", "a", s)
		if _, present := d.lastDesktopNotify["default|a"]; present {
			t.Errorf("status %q did not release the edge record; the session has moved on, "+
				"so its next prompt must alert again", s)
		}
	}
}

// The regression this closes, at the level the operator experiences it: a
// session prompts, the answer is handled fast enough that no running snapshot
// is observed, and it prompts again. Both prompts must alert.
func TestNotifyDesktop_ReAlertsWhenOnlyIdleSeenBetweenPrompts(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")

	rec := &recordingBackend{}
	d := &TransitionDaemon{
		deskNotifier:      desknotify.NewWithBackends(rec),
		lastDesktopNotify: map[string]string{},
	}
	inst := &Instance{ID: "a", Title: "flow"}

	d.notifyDesktop("default", inst, string(StatusWaiting))
	d.waitDesktopNotifications()
	// The daemon never observes running: the turn completed between polls.
	d.clearDesktopEdgeIfRunning("default", inst.ID, string(StatusIdle))
	d.notifyDesktop("default", inst, string(StatusWaiting))
	d.waitDesktopNotifications()

	if len(rec.titles) != 2 {
		t.Fatalf("delivered %d notifications, want 2: a second genuine prompt was "+
			"suppressed because only idle, not running, was seen in between", len(rec.titles))
	}
}

// notifyDesktop must tolerate a nil map: a TransitionDaemon built as a struct
// literal (as tests and any future call site may do) has no initialized maps,
// and a nil-map WRITE panics in Go.
func TestNotifyDesktop_NilMapDoesNotPanic(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")

	rec := &recordingBackend{}
	d := &TransitionDaemon{deskNotifier: desknotify.NewWithBackends(rec)} // lastDesktopNotify nil
	d.notifyDesktop("default", &Instance{ID: "a", Title: "flow"}, string(StatusWaiting))
	d.waitDesktopNotifications()

	if len(rec.titles) != 1 {
		t.Errorf("delivered %d notifications with a nil edge map, want 1", len(rec.titles))
	}
}

// A nil instance or nil notifier must not panic: notifyDesktop runs inside the
// status-poll loop, where a panic would take down session monitoring.
func TestNotifyDesktop_NilSafe(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")

	(&TransitionDaemon{}).notifyDesktop("default", &Instance{ID: "a"}, string(StatusWaiting))

	rec := &recordingBackend{}
	d := &TransitionDaemon{deskNotifier: desknotify.NewWithBackends(rec)}
	d.notifyDesktop("default", nil, string(StatusWaiting))
	d.waitDesktopNotifications()

	var nilDaemon *TransitionDaemon
	nilDaemon.notifyDesktop("default", &Instance{ID: "a"}, string(StatusWaiting))
	d.waitDesktopNotifications()
}
