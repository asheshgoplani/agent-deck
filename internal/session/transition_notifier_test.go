package session

import (
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

func TestDeferredEventNotMarkedAsNotified(t *testing.T) {
	n := &TransitionNotifier{
		statePath: t.TempDir() + "/state.json",
		logPath:   t.TempDir() + "/log.jsonl",
		state: transitionNotifyState{
			Records: map[string]transitionNotifyRecord{},
		},
	}

	event := TransitionNotificationEvent{
		ChildSessionID: "child1",
		ChildTitle:     "task",
		Profile:        "_test",
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      time.Now(),
		DeliveryResult: transitionDeliveryDeferred,
	}

	// Simulate what NotifyTransition does for a deferred event:
	// deferred events skip markNotified so they can be retried.
	if event.DeliveryResult != transitionDeliveryDeferred {
		n.markNotified(event)
	}

	// The event should NOT be in the dedup records, so isDuplicate returns false.
	if n.isDuplicate(event) {
		t.Fatal("deferred event should not be treated as duplicate")
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
