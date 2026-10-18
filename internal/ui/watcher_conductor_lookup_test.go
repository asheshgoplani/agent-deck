package ui

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestConductorPane_ReadsTitleUnderInstanceLock: the relay looks the
// conductor up off the UI goroutine while renames and title sync write Title
// under the instance's own lock. Run with -race: a plain Title read races.
func TestConductorPane_ReadsTitleUnderInstanceLock(t *testing.T) {
	inst := session.NewInstanceWithTool(session.ConductorSessionTitle("demo"), t.TempDir(), "shell")
	ts := tmux.NewSession("conductor-demo", t.TempDir())
	inst.SetTmuxSessionForTest(ts)
	home := NewHome()
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instancesMu.Unlock()

	stop := make(chan struct{})
	renamed := make(chan struct{})
	go func() {
		defer close(renamed)
		for i := 0; ; i++ {
			select {
			case <-stop:
				inst.SetTitleThreadSafe(session.ConductorSessionTitle("demo"))
				return
			default:
			}
			inst.SetTitleThreadSafe(session.ConductorSessionTitle("demo-" + strconv.Itoa(i%2)))
		}
	}()
	for i := 0; i < 1000; i++ {
		_, _ = home.conductorPane("demo")
	}
	close(stop)
	<-renamed
	if id, got := home.conductorPane("demo"); got != ts || id != inst.ID {
		t.Fatalf("conductor lookup returned %q %v, want the conductor's id and tmux session", id, got)
	}
}

// TestConductorPane_ReadsArchiveStateUnderInstanceLock: the relay
// looks the conductor up while background commands archive, unarchive and
// restart sessions from the archive. Run with -race: a plain ArchivedAt read
// or write races.
func TestConductorPane_ReadsArchiveStateUnderInstanceLock(t *testing.T) {
	inst := session.NewInstanceWithTool(session.ConductorSessionTitle("demo"), t.TempDir(), "shell")
	ts := tmux.NewSession("conductor-demo", t.TempDir())
	inst.SetTmuxSessionForTest(ts)
	inst.ArchivedAt = time.Now().UTC()
	home := NewHome()
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instancesMu.Unlock()

	persisted := func(*session.Instance) error { return nil }
	restartFails := func() error { return errors.New("restart failed") }
	stop := make(chan struct{})
	written := make(chan struct{})
	go func() {
		defer close(written)
		for {
			select {
			case <-stop:
				home.unarchiveSession(inst)()
				return
			default:
			}
			// A failed restart from the archive unarchives, then restores
			// the archive state: two writes per call.
			_, _ = restartWithArchiveTransition(inst, persisted, restartFails)
		}
	}()
	for i := 0; i < 1000; i++ {
		_, _ = home.conductorPane("demo")
	}
	close(stop)
	<-written
	if _, got := home.conductorPane("demo"); got != ts {
		t.Fatalf("conductor lookup after unarchive returned %v, want the conductor's tmux session", got)
	}
}

// TestConductorPane_HeadlessReadsStorage covers the headless engine
// owner (#2530): `web --no-tui` runs no Bubble Tea loop, so h.instances stays
// empty, and the conductor is found in storage on every delivery. The lookup
// leaves h.instances alone (a web mutation may be using it) and sees a
// conductor restarted on a new tmux session since the last delivery.
func TestConductorPane_HeadlessReadsStorage(t *testing.T) {
	home, storage := newHeadlessHomeForTest(t, "_test_2530_lookup_"+strconv.FormatInt(time.Now().UnixNano(), 36))
	inst := &session.Instance{
		ID:          "conductor-demo-id",
		Title:       session.ConductorSessionTitle("demo"),
		ProjectPath: t.TempDir(),
		GroupPath:   session.DefaultGroupPath,
		Command:     "bash",
		Tool:        "bash",
		Status:      session.StatusIdle,
		CreatedAt:   time.Now(),
	}
	save := func(tmuxName string) {
		t.Helper()
		inst.SetTmuxSessionForTest(&tmux.Session{Name: tmuxName})
		if err := storage.Save([]*session.Instance{inst}); err != nil {
			t.Fatal(err)
		}
	}

	if _, got := home.conductorPane("demo"); got != nil {
		t.Fatalf("lookup before the conductor exists = %q, want nil", got.Name)
	}
	save("agentdeck_conductor-demo_1")
	if id, got := home.conductorPane("demo"); got == nil || got.Name != "agentdeck_conductor-demo_1" || id != "conductor-demo-id" {
		t.Fatalf("headless lookup = %q %v, want the conductor's id and tmux session from storage", id, got)
	}
	save("agentdeck_conductor-demo_2")
	if _, got := home.conductorPane("demo"); got == nil || got.Name != "agentdeck_conductor-demo_2" {
		t.Fatalf("headless lookup after a restart = %v, want the new tmux session", got)
	}
	if _, got := home.conductorPane("other"); got != nil {
		t.Fatalf("lookup of an unknown conductor = %q, want nil", got.Name)
	}
	home.instancesMu.RLock()
	n := len(home.instances)
	home.instancesMu.RUnlock()
	if n != 0 {
		t.Fatalf("headless lookup loaded %d instances into Home, want it to leave them alone", n)
	}
}

// TestConductorPane_SkipsAnArchivedConductor: an archived session
// keeps the conductor's title. The lookup returns the live session's tmux
// session even when the archived one comes first, and nil when only the
// archived one is there, in a TUI and headless.
func TestConductorPane_SkipsAnArchivedConductor(t *testing.T) {
	conductor := func(id, tmuxName string, archived bool) *session.Instance {
		inst := &session.Instance{
			ID:          id,
			Title:       session.ConductorSessionTitle("demo"),
			ProjectPath: t.TempDir(),
			GroupPath:   session.DefaultGroupPath,
			Command:     "bash",
			Tool:        "bash",
			Status:      session.StatusIdle,
			CreatedAt:   time.Now(),
		}
		inst.SetTmuxSessionForTest(&tmux.Session{Name: tmuxName})
		if archived {
			inst.ArchivedAt = time.Now().UTC()
		}
		return inst
	}
	check := func(t *testing.T, home *Home, set func(...*session.Instance)) {
		t.Helper()
		archived := conductor("conductor-demo-old", "agentdeck_conductor-demo_old", true)
		live := conductor("conductor-demo-id", "agentdeck_conductor-demo_live", false)
		set(archived)
		if _, got := home.conductorPane("demo"); got != nil {
			t.Fatalf("lookup with only an archived conductor = %q, want nil", got.Name)
		}
		set(archived, live)
		if id, got := home.conductorPane("demo"); got == nil || got.Name != "agentdeck_conductor-demo_live" || id != "conductor-demo-id" {
			t.Fatalf("lookup with an archived conductor first = %q %v, want the live one's id and tmux session", id, got)
		}
	}

	t.Run("tui", func(t *testing.T) {
		home := NewHome()
		check(t, home, func(instances ...*session.Instance) {
			home.instancesMu.Lock()
			home.instances = instances
			home.instancesMu.Unlock()
		})
	})
	t.Run("headless", func(t *testing.T) {
		home, storage := newHeadlessHomeForTest(t, "_test_2530_archived_"+strconv.FormatInt(time.Now().UnixNano(), 36))
		check(t, home, func(instances ...*session.Instance) {
			if err := storage.Save(instances); err != nil {
				t.Fatal(err)
			}
		})
	})
}

// TestConductorTmuxSession_FindsRenamedConductorByStoredName: the stable
// conductor identity (stored ConductorName) wins over the title in a TUI and
// headless, so a renamed conductor still receives its watcher deliveries, and
// an archived session carrying the same stored name is still skipped.
func TestConductorTmuxSession_FindsRenamedConductorByStoredName(t *testing.T) {
	conductor := func(id, title, tmuxName string, archived bool) *session.Instance {
		inst := &session.Instance{
			ID:            id,
			Title:         title,
			IsConductor:   true,
			ConductorName: "demo",
			ProjectPath:   t.TempDir(),
			GroupPath:     session.DefaultGroupPath,
			Command:       "bash",
			Tool:          "bash",
			Status:        session.StatusIdle,
			CreatedAt:     time.Now(),
		}
		inst.SetTmuxSessionForTest(&tmux.Session{Name: tmuxName})
		if archived {
			inst.ArchivedAt = time.Now().UTC()
		}
		return inst
	}
	check := func(t *testing.T, home *Home, set func(...*session.Instance)) {
		t.Helper()
		archived := conductor("conductor-demo-old", "old boss", "agentdeck_conductor-demo_old", true)
		live := conductor("conductor-demo-id", "renamed boss", "agentdeck_conductor-demo_live", false)
		set(archived, live)
		if id, got := home.conductorPane("demo"); got == nil || got.Name != "agentdeck_conductor-demo_live" || id != "conductor-demo-id" {
			t.Fatalf("lookup of a renamed conductor = %q %v, want the live one's id and tmux session", id, got)
		}
		if _, got := home.conductorPane("other"); got != nil {
			t.Fatalf("lookup of an unknown conductor = %q, want nil", got.Name)
		}
	}

	t.Run("tui", func(t *testing.T) {
		home := NewHome()
		check(t, home, func(instances ...*session.Instance) {
			home.instancesMu.Lock()
			home.instances = instances
			home.instancesMu.Unlock()
		})
	})
	t.Run("headless", func(t *testing.T) {
		home, storage := newHeadlessHomeForTest(t, "_test_2694_renamed_"+strconv.FormatInt(time.Now().UnixNano(), 36))
		check(t, home, func(instances ...*session.Instance) {
			if err := storage.Save(instances); err != nil {
				t.Fatal(err)
			}
		})
	})
}
