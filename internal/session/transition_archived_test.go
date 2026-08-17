package session

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/desktopnotify"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func setDesktopNotificationsEnabled(t *testing.T, enabled bool) {
	t.Helper()
	configPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("[desktop_notifications]\nenabled = "+strconv.FormatBool(enabled)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()
}

func TestSyncProfile_LiveTUIArchivedStatusNeverReachesLastStatus(t *testing.T) {
	const profile = "_test_transition_archived_live_tui"
	d, storage := bootstrapDaemonProfile(t, profile)
	active := &Instance{ID: "live-active", Title: "active", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now()}
	archived := &Instance{ID: "live-archived", Title: "archived", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now(), ArchivedAt: time.Now().UTC()}
	if err := storage.SaveWithGroups([]*Instance{active, archived}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	db := storage.GetDB()
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("register TUI: %v", err)
	}
	if err := db.Heartbeat(); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := db.WriteStatus(active.ID, "running", active.Tool); err != nil {
		t.Fatalf("active status: %v", err)
	}
	if err := db.WriteStatus(archived.ID, "waiting", archived.Tool); err != nil {
		t.Fatalf("archived status: %v", err)
	}
	d.syncProfile(profile)
	if _, ok := d.lastStatus[profile][archived.ID]; ok {
		t.Fatal("archived live-TUI row reached lastStatus")
	}
	if got := d.lastStatus[profile][active.ID]; got != "running" {
		t.Fatalf("active status = %q, want running", got)
	}
}

func TestSyncProfile_InitialActionableStateSeedsDesktopBaseline(t *testing.T) {
	const profile = "_test_desktop_baseline"
	d, storage := bootstrapDaemonProfile(t, profile)
	setDesktopNotificationsEnabled(t, true)
	originalBaseline := desktopNotificationBaseline
	var baselines []desktopnotify.SourceEvent
	desktopNotificationBaseline = func(event desktopnotify.SourceEvent) error {
		baselines = append(baselines, event)
		return nil
	}
	t.Cleanup(func() { desktopNotificationBaseline = originalBaseline })

	inst := &Instance{ID: "baseline-error", Title: "baseline", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusError, CreatedAt: time.Now()}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	db := storage.GetDB()
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := db.Heartbeat(); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := db.WriteStatus(inst.ID, "error", inst.Tool); err != nil {
		t.Fatalf("status: %v", err)
	}

	d.syncProfile(profile)
	if len(baselines) != 1 || baselines[0].SessionID != inst.ID || baselines[0].ToStatus != "error" {
		t.Fatalf("initial desktop baselines = %+v, want one error state for %q", baselines, inst.ID)
	}
}

func TestSyncProfile_RetainsDesktopBaselinePersistenceFailure(t *testing.T) {
	const profile = "_test_desktop_baseline_error"
	d, storage := bootstrapDaemonProfile(t, profile)
	setDesktopNotificationsEnabled(t, true)
	wantErr := errors.New("state unavailable")
	originalBaseline := desktopNotificationBaseline
	desktopNotificationBaseline = func(desktopnotify.SourceEvent) error { return wantErr }
	t.Cleanup(func() { desktopNotificationBaseline = originalBaseline })
	inst := &Instance{ID: "baseline-error", Title: "baseline", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusError, CreatedAt: time.Now()}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	db := storage.GetDB()
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := db.Heartbeat(); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := db.WriteStatus(inst.ID, "error", inst.Tool); err != nil {
		t.Fatalf("status: %v", err)
	}

	d.syncProfile(profile)
	if !errors.Is(d.desktopNotificationBaselineErr[profile], wantErr) {
		t.Fatalf("desktop baseline error = %v, want %v", d.desktopNotificationBaselineErr[profile], wantErr)
	}
}

func TestSyncProfile_EnablingDesktopNotificationsSeedsFreshHookWithoutAlert(t *testing.T) {
	const profile = "_test_desktop_enable_hook_baseline"
	d, storage := bootstrapDaemonProfile(t, profile)
	setDesktopNotificationsEnabled(t, false)

	originalBaseline := desktopNotificationBaseline
	originalSender := desktopNotificationSender
	var baselines, delivered []desktopnotify.SourceEvent
	desktopNotificationBaseline = func(event desktopnotify.SourceEvent) error {
		baselines = append(baselines, event)
		return nil
	}
	desktopNotificationSender = func(event desktopnotify.SourceEvent) error {
		delivered = append(delivered, event)
		return nil
	}
	t.Cleanup(func() {
		desktopNotificationBaseline = originalBaseline
		desktopNotificationSender = originalSender
	})

	_, child := seedStaleRowFixture(t, storage, "enable-hook-child", "enable-hook-parent", "running")
	d.syncProfile(profile)
	if len(baselines) != 0 {
		t.Fatalf("disabled desktop notifications seeded %+v", baselines)
	}

	setDesktopNotificationsEnabled(t, true)
	seedHookStatusFile(t, child.ID, "Stop", "99999999-9999-9999-9999-999999999999", "waiting")
	d.syncProfile(profile)

	foundWaitingBaseline := false
	for _, event := range baselines {
		if event.SessionID == child.ID && event.ToStatus == "waiting" {
			foundWaitingBaseline = true
		}
	}
	if !foundWaitingBaseline {
		t.Fatalf("enable baseline = %+v, want fresh hook waiting state", baselines)
	}
	if len(delivered) != 0 {
		t.Fatalf("enable pass delivered stale desktop events %+v", delivered)
	}
}

func TestSyncProfile_RetriesDesktopBaselineAndWithholdsDispatchUntilReady(t *testing.T) {
	const profile = "_test_desktop_baseline_retry"
	d, storage := bootstrapDaemonProfile(t, profile)
	setDesktopNotificationsEnabled(t, true)

	originalBaseline := desktopNotificationBaseline
	originalSender := desktopNotificationSender
	defer func() {
		desktopNotificationBaseline = originalBaseline
		desktopNotificationSender = originalSender
	}()
	wantErr := errors.New("state unavailable")
	fail := true
	desktopNotificationBaseline = func(desktopnotify.SourceEvent) error {
		if fail {
			return wantErr
		}
		return nil
	}
	var delivered []desktopnotify.SourceEvent
	desktopNotificationSender = func(event desktopnotify.SourceEvent) error {
		delivered = append(delivered, event)
		return nil
	}

	inst := &Instance{ID: "baseline-retry", Title: "baseline", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now()}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	db := storage.GetDB()
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := db.Heartbeat(); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := db.WriteStatus(inst.ID, "running", inst.Tool); err != nil {
		t.Fatalf("initial status: %v", err)
	}

	d.syncProfile(profile)
	if !errors.Is(d.desktopNotificationBaselineErr[profile], wantErr) {
		t.Fatalf("first baseline error = %v, want %v", d.desktopNotificationBaselineErr[profile], wantErr)
	}
	if err := db.WriteStatus(inst.ID, "error", inst.Tool); err != nil {
		t.Fatalf("error status: %v", err)
	}
	fail = false
	d.syncProfile(profile)
	if d.desktopNotificationBaselineErr[profile] != nil {
		t.Fatalf("successful retry retained error: %v", d.desktopNotificationBaselineErr[profile])
	}
	if len(delivered) != 0 {
		t.Fatalf("baseline retry pass delivered %+v", delivered)
	}
	if err := db.WriteStatus(inst.ID, "waiting", inst.Tool); err != nil {
		t.Fatalf("waiting status: %v", err)
	}
	d.syncProfile(profile)
	if len(delivered) != 1 || delivered[0].ToStatus != "waiting" {
		t.Fatalf("post-baseline transition delivered %+v, want one waiting event", delivered)
	}
}

func TestArchiveAtPreCommitSeamSuppressesSnapshotHookAndDone(t *testing.T) {
	tests := []struct {
		name        string
		hook        *HookStatus
		probeStatus Status
	}{
		{name: "snapshot", probeStatus: StatusWaiting},
		{name: "hook", hook: &HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: time.Now()}, probeStatus: StatusRunning},
		{name: "done", hook: &HookStatus{Status: "running", Event: "Stop", UpdatedAt: time.Now(), DoneStatus: "ok", DoneSummary: "finished"}, probeStatus: StatusRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := "_test_archive_precommit_" + tt.name
			id, parentID := "precommit-"+tt.name, "precommit-parent-"+tt.name
			d, storage := bootstrapDaemonProfile(t, profile)
			child := &Instance{ID: id, Title: id, ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath, ParentSessionID: parentID, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour)}
			parent := &Instance{ID: parentID, Title: "parent", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour)}
			if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
				t.Fatalf("save: %v", err)
			}
			d.initialized[profile] = true
			d.lastStatus[profile] = map[string]string{id: "running"}
			if tt.hook != nil {
				d.hookWatcher = &StatusFileWatcher{statuses: map[string]*HookStatus{id: tt.hook}}
			}
			originalProbe := updateInstanceStatus.Load().(statusProbeFunc)
			updateInstanceStatus.Store(statusProbeFunc(func(inst *Instance) error {
				if inst.ID == id {
					inst.SetStatusThreadSafe(tt.probeStatus)
				}
				return nil
			}))
			t.Cleanup(func() { updateInstanceStatus.Store(originalProbe) })

			var once sync.Once
			var archiveErr error
			var seamCalls int
			d.beforeNotifierCommit = func(event TransitionNotificationEvent) {
				if event.ChildSessionID != id {
					return
				}
				seamCalls++
				once.Do(func() {
					archiveErr = storage.GetDB().SetArchived(id, time.Now().UTC())
				})
			}

			d.syncProfile(profile)
			if archiveErr != nil {
				t.Fatalf("archive at pre-commit seam: %v", archiveErr)
			}
			if seamCalls != 1 {
				t.Fatalf("pre-commit seam calls = %d, want 1", seamCalls)
			}
			events, err := DrainInboxForParent(parentID)
			if err != nil {
				t.Fatalf("drain parent inbox: %v", err)
			}
			if len(events) != 0 {
				t.Fatalf("archive at %s pre-commit seam allowed %d durable event(s)", tt.name, len(events))
			}
		})
	}
}

func TestDesktopDispatchRevalidatesNoTransitionNotifyAndArchiveForEverySource(t *testing.T) {
	sources := []struct {
		name        string
		hook        *HookStatus
		probeStatus Status
	}{
		{name: "snapshot", probeStatus: StatusWaiting},
		{name: "hook", hook: &HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: time.Now()}, probeStatus: StatusRunning},
		{name: "done", hook: &HookStatus{Status: "running", Event: "Stop", UpdatedAt: time.Now(), DoneStatus: "ok", DoneSummary: "finished"}, probeStatus: StatusRunning},
	}
	for _, guard := range []string{"no-transition-notify", "archived-at-dispatch"} {
		for _, source := range sources {
			t.Run(guard+"/"+source.name, func(t *testing.T) {
				profile := "_test_desktop_revalidate_" + guard + "_" + source.name
				id, parentID := "desktop-"+guard+"-"+source.name, "desktop-parent-"+guard+"-"+source.name
				d, storage := bootstrapDaemonProfile(t, profile)
				setDesktopNotificationsEnabled(t, true)
				child := &Instance{ID: id, Title: id, ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath, ParentSessionID: parentID, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour), NoTransitionNotify: guard == "no-transition-notify"}
				parent := &Instance{ID: parentID, Title: "parent", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour)}
				if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
					t.Fatalf("save: %v", err)
				}
				d.initialized[profile] = true
				d.desktopNotificationBaselineReady[profile] = true
				d.lastStatus[profile] = map[string]string{id: "running"}
				if source.hook != nil {
					d.hookWatcher = &StatusFileWatcher{statuses: map[string]*HookStatus{id: source.hook}}
				}
				originalSender := desktopNotificationSender
				var desktopEvents []desktopnotify.SourceEvent
				desktopNotificationSender = func(event desktopnotify.SourceEvent) error {
					desktopEvents = append(desktopEvents, event)
					return nil
				}
				t.Cleanup(func() { desktopNotificationSender = originalSender })
				if guard == "archived-at-dispatch" {
					var once sync.Once
					d.loadTransitionInstanceRow = func(_ string, instanceID string) (*statedb.InstanceRow, error) {
						if instanceID == id {
							once.Do(func() { _ = storage.GetDB().SetArchived(instanceID, time.Now().UTC()) })
						}
						return storage.GetDB().LoadInstanceByID(instanceID)
					}
				}
				originalProbe := updateInstanceStatus.Load().(statusProbeFunc)
				updateInstanceStatus.Store(statusProbeFunc(func(inst *Instance) error {
					if inst.ID == id {
						inst.SetStatusThreadSafe(source.probeStatus)
					}
					return nil
				}))
				t.Cleanup(func() { updateInstanceStatus.Store(originalProbe) })

				d.syncProfile(profile)
				for _, event := range desktopEvents {
					if event.SessionID == id {
						t.Fatalf("desktop %s %s dispatch emitted %+v", guard, source.name, desktopEvents)
					}
				}
			})
		}
	}
}

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

func TestHookTransitionArchivedRaceEmitsNoEvent(t *testing.T) {
	const profile = "_test_hook_transition_archive_race"
	const id = "hook-archive-race"
	const parentID = "hook-archive-race-parent"
	d, storage := bootstrapDaemonProfile(t, profile)
	child := &Instance{
		ID: id, Title: id, ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath,
		ParentSessionID: parentID, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour),
	}
	parent := &Instance{
		ID: parentID, Title: "parent", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath,
		Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour),
	}
	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("save hook-race instances: %v", err)
	}
	d.initialized[profile] = true
	d.lastStatus[profile] = map[string]string{id: "running"}
	d.hookWatcher = &StatusFileWatcher{statuses: map[string]*HookStatus{
		id: {Status: "waiting", Event: "Stop", UpdatedAt: time.Now()},
	}}

	probeStarted := make(chan struct{})
	archiveFinished := make(chan struct{})
	original := updateInstanceStatus.Load().(statusProbeFunc)
	updateInstanceStatus.Store(statusProbeFunc(func(loaded *Instance) error {
		if loaded.ID == id {
			close(probeStarted)
			<-archiveFinished
		}
		return nil // cached status remains running: snapshot transition cannot emit
	}))
	t.Cleanup(func() { updateInstanceStatus.Store(original) })

	done := make(chan struct{})
	go func() {
		d.syncProfile(profile)
		close(done)
	}()
	<-probeStarted
	if err := storage.GetDB().SetArchived(id, time.Now().UTC()); err != nil {
		t.Fatalf("persist archive while hook-only probe is blocked: %v", err)
	}
	close(archiveFinished)
	<-done

	events, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("drain parent inbox: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("archive racing a hook-only transition committed %d event(s)", len(events))
	}
}

func TestTransitionPersistedDisableRaceEmitsNoEvent(t *testing.T) {
	const profile = "_test_transition_persisted_disable_race"
	const id = "disable-race"
	const parentID = "disable-race-parent"
	d, storage := bootstrapDaemonProfile(t, profile)
	child := &Instance{
		ID: id, Title: id, ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath,
		ParentSessionID: parentID, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour),
	}
	parent := &Instance{
		ID: parentID, Title: "parent", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath,
		Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour),
	}
	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("save disable-race instances: %v", err)
	}
	d.initialized[profile] = true
	d.lastStatus[profile] = map[string]string{id: "running"}
	var emissionCount int
	d.observeTransitionEmission = func(TransitionNotificationEvent) { emissionCount++ }

	probeStarted := make(chan struct{})
	disableFinished := make(chan struct{})
	original := updateInstanceStatus.Load().(statusProbeFunc)
	updateInstanceStatus.Store(statusProbeFunc(func(loaded *Instance) error {
		if loaded.ID == id {
			close(probeStarted)
			<-disableFinished
			loaded.SetStatusThreadSafe(StatusWaiting)
		}
		return nil
	}))
	t.Cleanup(func() { updateInstanceStatus.Store(original) })

	done := make(chan struct{})
	go func() {
		d.syncProfile(profile)
		close(done)
	}()
	<-probeStarted
	row, err := storage.GetDB().LoadInstanceByID(id)
	if err != nil || row == nil {
		t.Fatalf("load persisted child row: row=%v err=%v", row, err)
	}
	row.NoTransitionNotify = true
	if err := storage.GetDB().SaveInstance(row); err != nil {
		t.Fatalf("persist transition notification disable: %v", err)
	}
	close(disableFinished)
	<-done

	events, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("drain parent inbox: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("persisted notification disable racing a transition committed %d event(s)", len(events))
	}
	if emissionCount != 0 {
		t.Fatalf("persisted notification disable reached daemon emission boundary %d time(s)", emissionCount)
	}
}

func TestTransitionRevalidationErrorRetriesSnapshotHookAndDone(t *testing.T) {
	tests := []struct {
		name        string
		hook        *HookStatus
		probeStatus Status
		wantDone    bool
	}{
		{name: "snapshot", probeStatus: StatusWaiting},
		{name: "hook", hook: &HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: time.Now()}, probeStatus: StatusRunning},
		{name: "done", hook: &HookStatus{Status: "running", Event: "Stop", UpdatedAt: time.Now(), DoneStatus: "ok", DoneSummary: "finished"}, probeStatus: StatusRunning, wantDone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := "_test_transition_revalidation_" + tt.name
			id, parentID := "retry-"+tt.name, "retry-parent-"+tt.name
			d, storage := bootstrapDaemonProfile(t, profile)
			child := &Instance{ID: id, Title: id, ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath, ParentSessionID: parentID, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour)}
			parent := &Instance{ID: parentID, Title: "parent", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now().Add(-time.Hour)}
			if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
				t.Fatalf("save: %v", err)
			}
			d.initialized[profile] = true
			d.lastStatus[profile] = map[string]string{id: "running"}
			if tt.hook != nil {
				d.hookWatcher = &StatusFileWatcher{statuses: map[string]*HookStatus{id: tt.hook}}
			}
			originalProbe := updateInstanceStatus.Load().(statusProbeFunc)
			updateInstanceStatus.Store(statusProbeFunc(func(inst *Instance) error {
				if inst.ID == id {
					inst.SetStatusThreadSafe(tt.probeStatus)
				}
				return nil
			}))
			t.Cleanup(func() { updateInstanceStatus.Store(originalProbe) })
			d.loadTransitionInstanceRow = func(string, string) (*statedb.InstanceRow, error) {
				return nil, errors.New("temporary sqlite read failure")
			}
			d.syncProfile(profile)
			if got := d.lastStatus[profile][id]; got != "running" {
				t.Fatalf("failed revalidation advanced lastStatus to %q", got)
			}
			if tt.wantDone {
				if _, ok := d.lastDone[profile][id]; ok {
					t.Fatal("failed revalidation consumed done signal")
				}
			}
			d.loadTransitionInstanceRow = nil
			var emitted int
			d.observeTransitionEmission = func(TransitionNotificationEvent) { emitted++ }
			d.syncProfile(profile)
			if tt.wantDone {
				if _, ok := d.lastDone[profile][id]; !ok {
					t.Fatal("done signal was not retried after revalidation recovered")
				}
			} else if emitted == 0 {
				t.Fatal("transition was not retried after revalidation recovered")
			}
		})
	}
}
