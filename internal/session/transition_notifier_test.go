package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestShouldNotifyTransition(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want bool
	}{
		{name: "running to waiting", from: "running", to: "waiting", want: true},
		{name: "running to error", from: "running", to: "error", want: true},
		{name: "running to idle", from: "running", to: "idle", want: true},
		{name: "waiting to running", from: "waiting", to: "running", want: false},
		{name: "same status", from: "running", to: "running", want: false},
		{name: "empty from", from: "", to: "waiting", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldNotifyTransition(tt.from, tt.to); got != tt.want {
				t.Fatalf("ShouldNotifyTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestChoosePollInterval(t *testing.T) {
	if got := choosePollInterval(map[string]string{"a": "running"}); got != notifyPollFast {
		t.Fatalf("running interval = %v, want %v", got, notifyPollFast)
	}
	if got := choosePollInterval(map[string]string{"a": "waiting"}); got != notifyPollMedium {
		t.Fatalf("waiting interval = %v, want %v", got, notifyPollMedium)
	}
	if got := choosePollInterval(map[string]string{"a": "idle"}); got != notifyPollSlow {
		t.Fatalf("idle interval = %v, want %v", got, notifyPollSlow)
	}
}

func TestResolveParentNotificationTargetMissingParentID(t *testing.T) {
	child := &Instance{ID: "child", Title: "task", ParentSessionID: ""}
	got := resolveParentNotificationTarget(child, map[string]*Instance{"child": child})
	if got != nil {
		t.Fatalf("expected nil for missing parent, got %#v", got)
	}
}

func TestResolveParentNotificationTargetParentNotFound(t *testing.T) {
	child := &Instance{ID: "child", Title: "task", ParentSessionID: "parent"}
	got := resolveParentNotificationTarget(child, map[string]*Instance{"child": child})
	if got != nil {
		t.Fatalf("expected nil for missing parent instance, got %#v", got)
	}
}

func TestResolveParentNotificationTargetReturnsParent(t *testing.T) {
	child := &Instance{ID: "child", Title: "task", ParentSessionID: "parent"}
	parent := &Instance{ID: "parent", Title: "manager", Status: StatusWaiting}
	byID := map[string]*Instance{
		"child":  child,
		"parent": parent,
	}
	got := resolveParentNotificationTarget(child, byID)
	if got == nil || got.ID != "parent" {
		t.Fatalf("expected parent target, got %#v", got)
	}
}

func TestResolveParentNotificationTargetSelfLoop(t *testing.T) {
	self := &Instance{ID: "self", Title: "task", ParentSessionID: "self"}
	byID := map[string]*Instance{"self": self}
	got := resolveParentNotificationTarget(self, byID)
	if got != nil {
		t.Fatalf("expected nil for self-referencing parent, got %#v", got)
	}
}

func TestTerminalHookTransitionCandidate(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		tool string
		hs   *HookStatus
		want bool
	}{
		{
			name: "claude stop terminal",
			tool: "claude",
			hs: &HookStatus{
				Status:    "waiting",
				Event:     "Stop",
				UpdatedAt: now,
			},
			want: true,
		},
		{
			name: "claude session start ignored",
			tool: "claude",
			hs: &HookStatus{
				Status:    "waiting",
				Event:     "SessionStart",
				UpdatedAt: now,
			},
			want: false,
		},
		{
			name: "codex turn complete terminal",
			tool: "codex",
			hs: &HookStatus{
				Status:    "waiting",
				Event:     "agent-turn-complete",
				UpdatedAt: now,
			},
			want: true,
		},
		{
			name: "codex turn start ignored",
			tool: "codex",
			hs: &HookStatus{
				Status:    "running",
				Event:     "agent-turn-start",
				UpdatedAt: now,
			},
			want: false,
		},
		{
			name: "stale hook ignored",
			tool: "codex",
			hs: &HookStatus{
				Status:    "waiting",
				Event:     "agent-turn-complete",
				UpdatedAt: now.Add(-2 * time.Minute),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := terminalHookTransitionCandidate(tt.tool, tt.hs)
			if got != tt.want {
				t.Fatalf("terminalHookTransitionCandidate(%q, %+v) = %v, want %v", tt.tool, tt.hs, got, tt.want)
			}
		})
	}
}

func TestIsCodexTerminalHookEvent(t *testing.T) {
	if !isCodexTerminalHookEvent("agent-turn-complete") {
		t.Fatal("expected terminal event to match")
	}
	if !isCodexTerminalHookEvent("turn/failed") {
		t.Fatal("expected failed event to match")
	}
	if isCodexTerminalHookEvent("thread.started") {
		t.Fatal("thread.started should not be terminal")
	}
}

func TestSyncProfileSkipsWhenInstanceNoTransitionNotify(t *testing.T) {
	child := &Instance{
		ID:                 "child-1",
		Title:              "worker",
		ParentSessionID:    "parent-1",
		NoTransitionNotify: true,
	}
	parent := &Instance{
		ID:     "parent-1",
		Title:  "orchestrator",
		Status: StatusWaiting,
	}
	byID := map[string]*Instance{
		"child-1":  child,
		"parent-1": parent,
	}

	// The child has NoTransitionNotify=true, so even though the transition
	// is valid (running→waiting), resolveParentNotificationTarget should
	// still return a parent — but the daemon guard should skip dispatch.
	// We test the guard logic indirectly: the parent resolution works,
	// meaning the guard is the only thing preventing dispatch.
	got := resolveParentNotificationTarget(child, byID)
	if got == nil {
		t.Fatal("parent should be resolvable (guard is in daemon, not here)")
	}

	// Verify the flag is set correctly
	if !child.NoTransitionNotify {
		t.Fatal("NoTransitionNotify should be true")
	}
}

// TestDispatchDropsEventWhenChildNoTransitionNotify is the strengthened
// regression test requested during PR #580 maintainer review: the weaker
// sibling above only proves the parent resolver is still reachable. This
// one proves the dispatch-level guard in transition_notifier.go:147 is the
// one dropping the event — even when (a) parent exists, (b) parent is
// live (StatusWaiting, not running → no defer), and (c) the event passes
// every earlier filter in NotifyTransition. Without the dispatch guard,
// SendSessionMessageReliable would fire and the event would come back as
// transitionDeliverySent, not transitionDeliveryDropped.
func TestDispatchDropsEventWhenChildNoTransitionNotify(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	// Ensure no ambient config leaks the flag state from the developer's machine.
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	// Seed the profile dir + config so GetEffectiveProfile resolves cleanly.
	if err := os.MkdirAll(tmpHome+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-no-transition-notify"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}

	now := time.Now()
	child := &Instance{
		ID:                 "child-1",
		Title:              "worker",
		ProjectPath:        "/tmp/child",
		GroupPath:          DefaultGroupPath,
		ParentSessionID:    "parent-1",
		Tool:               "shell",
		Status:             StatusWaiting,
		CreatedAt:          now,
		NoTransitionNotify: true, // <-- the guard under test
	}
	parent := &Instance{
		ID:          "parent-1",
		Title:       "orchestrator",
		ProjectPath: "/tmp/parent",
		GroupPath:   DefaultGroupPath,
		Tool:        "shell",
		Status:      StatusWaiting, // live, not running → dispatch() would NOT defer
		CreatedAt:   now,
	}

	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	notifier := NewTransitionNotifier()
	event := TransitionNotificationEvent{
		ChildSessionID: child.ID,
		ChildTitle:     child.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      now,
	}

	got := notifier.NotifyTransition(event)

	// The guard at transition_notifier.go:147 must short-circuit to dropped.
	// If a future refactor removes that guard, dispatch would proceed to
	// SendSessionMessageReliable (which returns sent or failed depending on
	// tmux availability in the test env), making this test fail.
	if got.DeliveryResult != transitionDeliveryDropped {
		t.Fatalf("dispatch must drop event when child.NoTransitionNotify=true, got DeliveryResult=%q", got.DeliveryResult)
	}
	// Target fields must remain zero — the drop happens before parent resolution wires them.
	if got.TargetSessionID != "" || got.TargetKind != "" {
		t.Fatalf("dropped event must not be tagged with a target, got TargetSessionID=%q TargetKind=%q",
			got.TargetSessionID, got.TargetKind)
	}
}

func TestInstanceNoTransitionNotifyJSONRoundTrip(t *testing.T) {
	inst := &Instance{
		ID:                 "test-1",
		Title:              "test",
		NoTransitionNotify: true,
	}

	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify field is present in JSON
	if !strings.Contains(string(data), `"no_transition_notify":true`) {
		t.Fatalf("expected no_transition_notify in JSON, got: %s", data)
	}

	// Verify omitempty: false value should be omitted
	inst2 := &Instance{ID: "test-2", Title: "test2"}
	data2, err := json.Marshal(inst2)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data2), "no_transition_notify") {
		t.Fatalf("no_transition_notify should be omitted when false, got: %s", data2)
	}

	// Round-trip
	var decoded Instance
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !decoded.NoTransitionNotify {
		t.Fatal("NoTransitionNotify should be true after round-trip")
	}
}
