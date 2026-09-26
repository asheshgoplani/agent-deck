package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// titleResolver is a minimal Resolver: exact title or exact id.
func titleResolver(identifier string, instances []*session.Instance) (*session.Instance, string, string) {
	for _, inst := range instances {
		if inst.Title == identifier || inst.ID == identifier {
			return inst, "", ""
		}
	}
	return nil, fmt.Sprintf("session '%s' not found", identifier), CodeNotFound
}

func testRegistry(t *testing.T, deps Deps) *Registry {
	t.Helper()
	if deps.Resolve == nil {
		deps.Resolve = titleResolver
	}
	r := NewRegistry()
	if err := RegisterBuiltins(r, deps); err != nil {
		t.Fatal(err)
	}
	return r
}

// seedStore writes instances and groups into profile's store.
func seedStore(t *testing.T, profile string, groups []*session.GroupData, instances ...*session.Instance) {
	t.Helper()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, groups)); err != nil {
		t.Fatal(err)
	}
}

func loadStore(t *testing.T, profile string) []*session.Instance {
	t.Helper()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	return instances
}

func findByTitle(instances []*session.Instance, title string) *session.Instance {
	for _, inst := range instances {
		if inst.Title == title {
			return inst
		}
	}
	return nil
}

func wantCode(t *testing.T, err error, code, msg string) {
	t.Helper()
	ce := AsError(err)
	if ce == nil {
		t.Fatalf("err = nil, want %s", code)
	}
	if ce.Code != code {
		t.Fatalf("code = %s (%q), want %s", ce.Code, ce.Message, code)
	}
	if msg != "" && ce.Message != msg {
		t.Fatalf("message = %q, want %q", ce.Message, msg)
	}
}

func TestSessionStartUnknownSessionIsNotFound(t *testing.T) {
	const profile = "_core_start_nf"
	seedStore(t, profile, nil, session.NewInstance("alpha", t.TempDir()))
	res := testRegistry(t, Deps{}).Run(context.Background(), IDSessionStart, SessionStartIn{Profile: profile, Session: "nosuch"})
	wantCode(t, res.Err, CodeNotFound, "session 'nosuch' not found")
}

func TestSessionStartQueuesWhenGroupAtCap(t *testing.T) {
	const profile = "_core_start_queue"
	dir := t.TempDir()
	running := session.NewInstanceWithGroup("busy", dir, "serial")
	running.Status = session.StatusRunning
	waiting := session.NewInstanceWithGroup("next", dir, "serial")
	waiting.Status = session.StatusStopped
	seedStore(t, profile, []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}, running, waiting)

	out, res := Invoke[SessionStartOut](context.Background(), testRegistry(t, Deps{}), IDSessionStart, SessionStartIn{Profile: profile, Session: "next"})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if out.Status != StartStatusQueued || out.MaxConcurrent != 1 || out.Group != "serial" || out.Title != "next" || out.ID != waiting.ID {
		t.Fatalf("out = %+v", out)
	}
	if got := findByTitle(loadStore(t, profile), "next"); got == nil || got.Status != session.StatusQueued {
		t.Fatalf("persisted status = %+v, want queued", got)
	}
}

func TestSessionStartYoloFailureIsInvalidAndStartsNothing(t *testing.T) {
	const profile = "_core_start_yolo"
	seedStore(t, profile, nil, session.NewInstance("alpha", t.TempDir()))
	deps := Deps{ApplyYolo: func(*session.Instance, bool) error { return errors.New("yolo unsupported") }}
	res := testRegistry(t, deps).Run(context.Background(), IDSessionStart, SessionStartIn{Profile: profile, Session: "alpha", Yolo: true})
	wantCode(t, res.Err, CodeInvalid, "yolo unsupported")
}

func TestSessionStopNotRunning(t *testing.T) {
	const profile = "_core_stop_idle"
	seedStore(t, profile, nil, session.NewInstance("alpha", t.TempDir()))
	res := testRegistry(t, Deps{}).Run(context.Background(), IDSessionStop, SessionStopIn{Profile: profile, Session: "alpha"})
	wantCode(t, res.Err, CodeNotRunning, "session 'alpha' is not running")
	res.Finish() // nothing deferred on failure
}

func TestSessionStopUnknownSession(t *testing.T) {
	const profile = "_core_stop_nf"
	seedStore(t, profile, nil, session.NewInstance("alpha", t.TempDir()))
	res := testRegistry(t, Deps{}).Run(context.Background(), IDSessionStop, SessionStopIn{Profile: profile, Session: "beta"})
	wantCode(t, res.Err, CodeNotFound, "session 'beta' not found")
}

func TestSessionRestartRequiresIdentifier(t *testing.T) {
	const profile = "_core_restart_noid"
	seedStore(t, profile, nil, session.NewInstance("alpha", t.TempDir()))
	res := testRegistry(t, Deps{}).Run(context.Background(), IDSessionRestart, SessionRestartIn{Profile: profile})
	wantCode(t, res.Err, CodeMissingArg, "session identifier required (or use --all)")
}

func TestSessionRestartAllWithNothingActive(t *testing.T) {
	const profile = "_core_restart_none"
	seedStore(t, profile, nil, session.NewInstance("alpha", t.TempDir()))
	res := testRegistry(t, Deps{}).Run(context.Background(), IDSessionRestart, SessionRestartIn{Profile: profile, All: true})
	wantCode(t, res.Err, CodeNoActive, "no active sessions to restart")
}

func TestSessionRestartSkipsFreshHealthySession(t *testing.T) {
	const profile = "_core_restart_fresh"
	inst := session.NewInstance("alpha", t.TempDir())
	inst.Status = session.StatusRunning
	inst.LastStartedAt = time.Now()
	seedStore(t, profile, nil, inst)

	out, res := Invoke[SessionRestartOut](context.Background(), testRegistry(t, Deps{}), IDSessionRestart, SessionRestartIn{Profile: profile, Session: "alpha"})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if !out.Skipped || out.Reason == "" || out.ID != inst.ID || out.All != nil {
		t.Fatalf("out = %+v, want a skipped single restart", out)
	}
}

func TestSessionListStaticRows(t *testing.T) {
	const profile = "_core_list_static"
	parent := session.NewInstanceWithGroup("parent", t.TempDir(), "work")
	child := session.NewInstanceWithGroup("child", t.TempDir(), "work/api")
	child.ParentSessionID = parent.ID
	child.Account = "slot-a"
	seedStore(t, profile, nil, parent, child)

	out, res := Invoke[SessionListOut](context.Background(), testRegistry(t, Deps{}), IDSessionList, SessionListIn{Profile: profile})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if out.Profile != profile || len(out.Sessions) != 2 || out.Stats != nil {
		t.Fatalf("out = %+v", out)
	}
	var row SessionRow
	for _, r := range out.Sessions {
		if r.Title == "child" {
			row = r
		}
	}
	if row.ParentSessionID != parent.ID || row.ParentProjectPath != parent.ProjectPath {
		t.Fatalf("parent path not recovered: %+v", row)
	}
	if row.Group != "work/api" || row.Account != "slot-a" || row.Profile != profile || row.LastActivityAt != "" {
		t.Fatalf("static row = %+v", row)
	}
}

func TestSessionListHidesSupersededUnlessAsked(t *testing.T) {
	const profile = "_core_list_superseded"
	live := session.NewInstance("live", t.TempDir())
	old := session.NewInstance("old", t.TempDir())
	old.SupersededBy = live.ID
	old.ArchivedAt = time.Now()
	seedStore(t, profile, nil, live, old)

	r := testRegistry(t, Deps{})
	out, res := Invoke[SessionListOut](context.Background(), r, IDSessionList, SessionListIn{Profile: profile})
	if res.Err != nil || len(out.Sessions) != 1 || out.Sessions[0].Title != "live" {
		t.Fatalf("default list = %+v (%v)", out.Sessions, res.Err)
	}
	out, res = Invoke[SessionListOut](context.Background(), r, IDSessionList, SessionListIn{Profile: profile, IncludeSuperseded: true})
	if res.Err != nil || len(out.Sessions) != 2 {
		t.Fatalf("include_superseded list = %+v (%v)", out.Sessions, res.Err)
	}
}

func TestSessionListEmptyProfile(t *testing.T) {
	const profile = "_core_list_empty"
	seedStore(t, profile, nil)
	out, res := Invoke[SessionListOut](context.Background(), testRegistry(t, Deps{}), IDSessionList, SessionListIn{Profile: profile, LiveStatus: true})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if out.Profile != profile || out.Sessions == nil || len(out.Sessions) != 0 || out.Stats != nil {
		t.Fatalf("out = %+v", out)
	}
}

func TestSessionListLiveStatusFillsLiveFields(t *testing.T) {
	const profile = "_core_list_live"
	inst := session.NewInstance("alpha", t.TempDir())
	seedStore(t, profile, nil, inst)
	out, res := Invoke[SessionListOut](context.Background(), testRegistry(t, Deps{}), IDSessionList, SessionListIn{Profile: profile, LiveStatus: true})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(out.Sessions) != 1 || out.Stats == nil || out.Stats.Sessions != 1 {
		t.Fatalf("out = %+v", out)
	}
	if out.Sessions[0].LastActivityAt == "" || out.Sessions[0].Status == "" {
		t.Fatalf("live fields missing: %+v", out.Sessions[0])
	}
}

func TestSessionListMarksStoredStoppedStatus(t *testing.T) {
	const profile = "_core_list_cached_stopped"
	stopped := session.NewInstance("stopped", t.TempDir())
	stopped.Status = session.StatusStopped
	live := session.NewInstance("live", t.TempDir())
	seedStore(t, profile, nil, stopped, live)
	out, res := Invoke[SessionListOut](context.Background(), testRegistry(t, Deps{}), IDSessionList, SessionListIn{Profile: profile, LiveStatus: true})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(out.Sessions) != 2 || out.Sessions[0].StatusSource != "cached" || out.Sessions[0].Status != "stopped" || out.Sessions[1].StatusSource != "live" {
		t.Fatalf("status evidence = %+v", out.Sessions)
	}
}

func TestSessionListAllProfilesCountsEveryProfile(t *testing.T) {
	seedStore(t, "_core_all_a", nil, session.NewInstance("a1", t.TempDir()))
	seedStore(t, "_core_all_b", nil)
	out, res := Invoke[SessionListOut](context.Background(), testRegistry(t, Deps{}), IDSessionList, SessionListIn{AllProfiles: true})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if out.ProfileCount < 2 || out.ProfileCount < len(out.Profiles) {
		t.Fatalf("profile count %d, profiles %d", out.ProfileCount, len(out.Profiles))
	}
	seen := map[string]int{}
	for _, p := range out.Profiles {
		seen[p.Profile] = len(p.Sessions)
	}
	if seen["_core_all_a"] != 1 || seen["_core_all_b"] != 0 {
		t.Fatalf("per-profile counts = %v", seen)
	}
}

func TestGroupListTreeFlatAndTotals(t *testing.T) {
	const profile = "_core_group_list"
	dir := t.TempDir()
	groups := []*session.GroupData{
		{Name: "work", Path: "work", Order: 0},
		{Name: "api", Path: "work/api", Order: 0},
		{Name: "empty", Path: "empty", Order: 1},
	}
	seedStore(t, profile, groups,
		session.NewInstanceWithGroup("w1", dir, "work"),
		session.NewInstanceWithGroup("a1", dir, "work/api"),
		session.NewInstanceWithGroup("a2", dir, "work/api"),
	)

	out, res := Invoke[GroupListOut](context.Background(), testRegistry(t, Deps{}), IDGroupList, GroupListIn{Profile: profile})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if out.TotalGroups != 3 || out.TotalSessions != 3 {
		t.Fatalf("totals = %d groups, %d sessions", out.TotalGroups, out.TotalSessions)
	}
	var work, empty *GroupNode
	for i := range out.Groups {
		switch out.Groups[i].Path {
		case "work":
			work = &out.Groups[i]
		case "empty":
			empty = &out.Groups[i]
		}
	}
	if work == nil || empty == nil || len(out.Groups) != 2 {
		t.Fatalf("roots = %+v", out.Groups)
	}
	if work.SessionCount != 3 || work.Status == nil || len(work.Children) != 1 || work.Children[0].Path != "work/api" || work.Children[0].SessionCount != 2 {
		t.Fatalf("work subtree = %+v", work)
	}
	if empty.SessionCount != 0 || empty.Status != nil {
		t.Fatalf("empty group = %+v", empty)
	}
	levels := map[string]int{}
	for _, g := range out.Flat {
		levels[g.Path] = g.Level
	}
	if len(out.Flat) != 3 || levels["work"] != 0 || levels["work/api"] != 1 || levels["empty"] != 0 {
		t.Fatalf("flat = %+v", out.Flat)
	}
}

func TestDrainGroupQueueWithNothingQueued(t *testing.T) {
	inst := session.NewInstanceWithGroup("a", t.TempDir(), "g")
	inst.Status = session.StatusStopped
	var events []Event
	ctx := WithObserver(context.Background(), func(ev Event) { events = append(events, ev) })
	if got := drainGroupQueue(ctx, "g", []*session.Instance{inst}, []*session.GroupData{{Name: "g", Path: "g", MaxConcurrent: 1}}); got != nil {
		t.Fatalf("drained %v with nothing queued", got.Title)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v", events)
	}
}

func TestDrainGroupQueueRespectsCap(t *testing.T) {
	dir := t.TempDir()
	running := session.NewInstanceWithGroup("run", dir, "g")
	running.Status = session.StatusRunning
	queued := session.NewInstanceWithGroup("q", dir, "g")
	queued.Status = session.StatusQueued
	got := drainGroupQueue(context.Background(), "g", []*session.Instance{running, queued}, []*session.GroupData{{Name: "g", Path: "g", MaxConcurrent: 1}})
	if got != nil || queued.Status != session.StatusQueued {
		t.Fatalf("drained at cap: %v, queued status %s", got, queued.Status)
	}
}

func TestNewSpawnFailureReasons(t *testing.T) {
	inst := &session.Instance{ID: "id1", Title: "t"}
	rec := &session.SpawnFailureRecord{Reason: "spawn_died_fast", ElapsedMs: 5}
	cases := []struct {
		err        error
		tmux, want string
		record     bool
	}{
		{&session.SpawnFailedError{TmuxName: "agentdeck_t", Record: rec}, "agentdeck_t", "spawn_died_fast", true},
		{&session.SpawnFailedError{TmuxName: "agentdeck_t"}, "agentdeck_t", "tmux_session_missing", false},
		{fmt.Errorf("wrapped: %w", &session.SpawnFailedError{TmuxName: "x"}), "x", "tmux_session_missing", false},
		{errors.New("probe timed out"), "", "spawn_unverified", false},
	}
	for _, c := range cases {
		sf := newSpawnFailure("restart", inst, c.err)
		if sf.ID != "id1" || sf.Title != "t" || sf.Verb != "restart" || sf.Tmux != c.tmux || sf.Reason != c.want || (sf.Record != nil) != c.record {
			t.Errorf("newSpawnFailure(%v) = %+v", c.err, sf)
		}
	}
	if got := spawnFailureMessage("start", errors.New("boom")); got != "failed to start session: boom" {
		t.Fatalf("message = %q", got)
	}
}
