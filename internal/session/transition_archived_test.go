package session

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstanceAcceptsTransitionEvents_ArchivedPredicate(t *testing.T) {
	tests := []struct {
		name string
		inst *Instance
		want bool
	}{
		{name: "nil", inst: nil, want: false},
		{name: "disabled", inst: &Instance{NoTransitionNotify: true}, want: false},
		{name: "archived", inst: &Instance{ArchivedAt: time.Now()}, want: false},
		{name: "active", inst: &Instance{}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := instanceAcceptsTransitionEvents(tt.inst); got != tt.want {
				t.Fatalf("instanceAcceptsTransitionEvents() = %v, want %v", got, tt.want)
			}
		})
	}
}

func saveArchivedTransitionInstance(t *testing.T, storage *Storage, id string) {
	t.Helper()
	inst := &Instance{
		ID:          id,
		Title:       id,
		ProjectPath: filepath.Join(t.TempDir(), "project"),
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   time.Now().Add(-time.Hour),
		ArchivedAt:  time.Now().UTC(),
	}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save archived instance: %v", err)
	}
}

func withCountingTransitionProbe(t *testing.T) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	original := updateInstanceStatus.Load().(statusProbeFunc)
	updateInstanceStatus.Store(statusProbeFunc(func(*Instance) error {
		calls.Add(1)
		return nil
	}))
	t.Cleanup(func() { updateInstanceStatus.Store(original) })
	return &calls
}

func TestSyncProfile_ArchivedHookStateIsNeverProbedOrEmitted(t *testing.T) {
	const profile = "_test_transition_archived_hook"
	const id = "archived-hook"
	d, storage := bootstrapDaemonProfile(t, profile)
	saveArchivedTransitionInstance(t, storage, id)
	probes := withCountingTransitionProbe(t)
	d.hookWatcher = &StatusFileWatcher{statuses: map[string]*HookStatus{
		id: {
			Status:      "waiting",
			Event:       "Stop",
			UpdatedAt:   time.Now(),
			DoneStatus:  "ok",
			DoneSummary: "finished",
		},
	}}

	d.syncProfile(profile)

	if got := probes.Load(); got != 0 {
		t.Fatalf("archived hook instance was externally probed %d times", got)
	}
	if _, ok := d.lastStatus[profile][id]; ok {
		t.Fatal("archived hook instance reached lastStatus")
	}
	if _, ok := d.lastDone[profile][id]; ok {
		t.Fatal("archived hook instance emitted a done signal")
	}
}

func TestSyncProfile_ArchivedVanishedTmuxIsNeverProbedOrEmitted(t *testing.T) {
	const profile = "_test_transition_archived_vanished"
	const id = "archived-vanished"
	d, storage := bootstrapDaemonProfile(t, profile)
	saveArchivedTransitionInstance(t, storage, id)
	probes := withCountingTransitionProbe(t)

	d.syncProfile(profile)

	if got := probes.Load(); got != 0 {
		t.Fatalf("archived vanished-tmux instance was externally probed %d times", got)
	}
	if _, ok := d.lastStatus[profile][id]; ok {
		t.Fatal("archived vanished-tmux instance reached lastStatus")
	}
	if _, ok := d.notifier.state.Records[id]; ok {
		t.Fatal("archived vanished-tmux instance emitted a transition")
	}
}

func TestSyncProfile_ArchivedInstanceNeverReachesSelfHealInputs(t *testing.T) {
	const profile = "_test_transition_archived_selfheal"
	const archivedID = "archived-selfheal"
	const activeID = "active-selfheal"
	d, storage := bootstrapDaemonProfile(t, profile)
	archived := &Instance{
		ID: archivedID, Title: archivedID, ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath,
		Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour), ArchivedAt: time.Now().UTC(),
	}
	active := &Instance{
		ID: activeID, Title: activeID, ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath,
		Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour),
	}
	if err := storage.SaveWithGroups([]*Instance{archived, active}, nil); err != nil {
		t.Fatalf("save self-heal instances: %v", err)
	}
	withCountingTransitionProbe(t)

	var observed []string
	d.observeSelfHealInputs = func(instances []*Instance) {
		for _, inst := range instances {
			observed = append(observed, inst.ID)
		}
	}
	d.syncProfile(profile)

	if len(observed) != 1 || observed[0] != activeID {
		t.Fatalf("self-heal inputs = %v, want only active instance %q", observed, activeID)
	}
}

func TestTransitionArchivedRaceEmitsNoTransitionOrDoneEvent(t *testing.T) {
	const profile = "_test_transition_archive_race"
	const id = "archive-race"
	const parentID = "archive-race-parent"
	d, storage := bootstrapDaemonProfile(t, profile)
	inst := &Instance{
		ID: id, Title: id, ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath,
		ParentSessionID: parentID, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour),
	}
	parent := &Instance{
		ID: parentID, Title: "parent", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath,
		Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour),
	}
	if err := storage.SaveWithGroups([]*Instance{inst, parent}, nil); err != nil {
		t.Fatalf("save active instance: %v", err)
	}
	d.initialized[profile] = true
	d.lastStatus[profile] = map[string]string{id: "running"}
	d.hookWatcher = &StatusFileWatcher{statuses: map[string]*HookStatus{
		id: {Status: "waiting", Event: "Stop", UpdatedAt: time.Now(), DoneStatus: "ok", DoneSummary: "finished"},
	}}

	probeStarted := make(chan *Instance, 1)
	archiveFinished := make(chan struct{})
	original := updateInstanceStatus.Load().(statusProbeFunc)
	updateInstanceStatus.Store(statusProbeFunc(func(loaded *Instance) error {
		if loaded.ID != id {
			return nil
		}
		probeStarted <- loaded
		<-archiveFinished
		loaded.SetStatusThreadSafe(StatusWaiting)
		return nil
	}))
	t.Cleanup(func() { updateInstanceStatus.Store(original) })

	done := make(chan struct{})
	go func() {
		d.syncProfile(profile)
		close(done)
	}()
	<-probeStarted
	if err := storage.GetDB().SetArchived(id, time.Now().UTC()); err != nil {
		t.Fatalf("persist archive while transition probe is blocked: %v", err)
	}
	close(archiveFinished)
	<-done

	if _, ok := d.notifier.state.Records[id]; ok {
		t.Fatal("archive racing a transition emitted a transition event")
	}
	events, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("drain parent inbox: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("archive racing a transition committed %d parent inbox event(s)", len(events))
	}
	if _, ok := d.lastDone[profile][id]; ok {
		t.Fatal("archive racing a transition emitted a done event")
	}
}
