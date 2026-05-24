package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestKanbanWatcher_CountsStartAtZero verifies that a newly created watcher
// reports zero for both running and blocked counts before any events are applied.
func TestKanbanWatcher_CountsStartAtZero(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0") // unreachable; no Start()
	running, blocked := w.Counts()
	if running != 0 {
		t.Errorf("running = %d, want 0", running)
	}
	if blocked != 0 {
		t.Errorf("blocked = %d, want 0", blocked)
	}
}

// TestKanbanWatcher_StopIsIdempotent verifies that calling Stop() multiple times
// does not panic or deadlock.
func TestKanbanWatcher_StopIsIdempotent(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.Stop()
	w.Stop() // second call must not panic
	w.Stop() // third call must not panic
}

// TestKanbanWatcher_SubscribeNotifies verifies that a subscriber channel
// receives a notification when a count-changing event is applied.
func TestKanbanWatcher_SubscribeNotifies(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	ch := w.Subscribe()

	// Apply a "claimed" event which increments running count.
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "task-1"})

	select {
	case <-ch:
		// good
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber notification after claimed event")
	}

	// Verify count changed.
	running, _ := w.Counts()
	if running != 1 {
		t.Errorf("running = %d, want 1 after claimed event", running)
	}
}

// TestKanbanWatcher_SubscribeNoNotifyOnNoChange verifies that applying an event
// that does not change counts does not notify.
// "unblocked" for a task with no prior tracked state is a stale/out-of-order
// event — we have no proof it was blocked, so counts must not change.
func TestKanbanWatcher_SubscribeNoNotifyOnNoChange(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	ch := w.Subscribe()

	// "unblocked" for an unseen task — must be a no-op.
	w.applyEvent(kanbanEvent{ID: 1, Kind: "unblocked", TaskID: "task-1"})

	select {
	case <-ch:
		t.Error("received unexpected notification: unblocked for unseen task should be a no-op")
	case <-time.After(50 * time.Millisecond):
		// good — no spurious notification
	}
	running, blocked := w.Counts()
	if running != 0 || blocked != 0 {
		t.Errorf("counts after unseen-task unblocked: running=%d blocked=%d, want 0 0", running, blocked)
	}
}

// TestKanbanWatcher_ApplyEventClaimed verifies running increments on "claimed".
func TestKanbanWatcher_ApplyEventClaimed(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed"})
	running, blocked := w.Counts()
	if running != 1 || blocked != 0 {
		t.Errorf("after claimed: running=%d blocked=%d, want 1 0", running, blocked)
	}
}

// TestKanbanWatcher_ApplyEventCompleted verifies running decrements on "completed".
func TestKanbanWatcher_ApplyEventCompleted(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "completed"})
	running, blocked := w.Counts()
	if running != 0 || blocked != 0 {
		t.Errorf("after claimed+completed: running=%d blocked=%d, want 0 0", running, blocked)
	}
}

// TestKanbanWatcher_ApplyEventBlocked verifies blocked increments on "blocked".
func TestKanbanWatcher_ApplyEventBlocked(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "blocked"})
	running, blocked := w.Counts()
	if running != 0 || blocked != 1 {
		t.Errorf("after blocked: running=%d blocked=%d, want 0 1", running, blocked)
	}
}

// TestKanbanWatcher_ApplyEventUnblocked verifies that blocked→running transition
// correctly swaps counters. After blocked+unblocked the task is running (not gone).
func TestKanbanWatcher_ApplyEventUnblocked(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	// Use a task ID so state is tracked. No-ID path uses best-effort swap.
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "t_x"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "blocked", TaskID: "t_x"})
	w.applyEvent(kanbanEvent{ID: 3, Kind: "unblocked", TaskID: "t_x"})
	running, blocked := w.Counts()
	if running != 1 || blocked != 0 {
		t.Errorf("after claimed+blocked+unblocked: running=%d blocked=%d, want 1 0", running, blocked)
	}
}

// TestKanbanWatcher_ApplyEventCrashed verifies running decrements on "crashed".
func TestKanbanWatcher_ApplyEventCrashed(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "crashed"})
	running, blocked := w.Counts()
	if running != 0 || blocked != 0 {
		t.Errorf("after claimed+crashed: running=%d blocked=%d, want 0 0", running, blocked)
	}
}

// TestKanbanWatcher_ApplyEvent_Reclaimed_FromRunning verifies reclaimed running task stays running.
func TestKanbanWatcher_ApplyEvent_Reclaimed_FromRunning(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "t1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "reclaimed", TaskID: "t1"})
	running, blocked := w.Counts()
	if running != 1 || blocked != 0 {
		t.Errorf("after claimed+reclaimed: running=%d blocked=%d, want 1 0", running, blocked)
	}
	if w.TaskStatus("t1") != "running" {
		t.Errorf("TaskStatus after reclaimed = %q, want running", w.TaskStatus("t1"))
	}
}

// TestKanbanWatcher_ApplyEvent_Reclaimed_FromBlocked verifies blocked→running on reclaim.
func TestKanbanWatcher_ApplyEvent_Reclaimed_FromBlocked(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "t1"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "blocked", TaskID: "t1"})
	w.applyEvent(kanbanEvent{ID: 3, Kind: "reclaimed", TaskID: "t1"})
	running, blocked := w.Counts()
	if running != 1 || blocked != 0 {
		t.Errorf("after claimed+blocked+reclaimed: running=%d blocked=%d, want 1 0", running, blocked)
	}
}

// TestKanbanWatcher_Unsubscribe verifies that Unsubscribe removes the channel from subs.
func TestKanbanWatcher_Unsubscribe(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	ch1 := w.Subscribe()
	ch2 := w.Subscribe()

	w.Unsubscribe(ch1)

	w.subsMu.Lock()
	n := len(w.subs)
	w.subsMu.Unlock()

	if n != 1 {
		t.Errorf("after Unsubscribe: subs len=%d, want 1", n)
	}
	_ = ch2
}

// TestKanbanWatcher_NeverNegative verifies counts never go below zero.
func TestKanbanWatcher_NeverNegative(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	// Apply events that would underflow if not guarded.
	w.applyEvent(kanbanEvent{ID: 1, Kind: "completed"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "crashed"})
	w.applyEvent(kanbanEvent{ID: 3, Kind: "unblocked"})
	running, blocked := w.Counts()
	if running < 0 || blocked < 0 {
		t.Errorf("counts went negative: running=%d blocked=%d", running, blocked)
	}
}

// TestKanbanWatcher_BuildWSURL verifies URL conversion from HTTP to WebSocket.
func TestKanbanWatcher_BuildWSURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://127.0.0.1:8080", "ws://127.0.0.1:8080/api/plugins/kanban/events"},
		{"https://example.com", "wss://example.com/api/plugins/kanban/events"},
		{"ws://127.0.0.1:9000", "ws://127.0.0.1:9000/api/plugins/kanban/events"},
		{"wss://example.com", "wss://example.com/api/plugins/kanban/events"},
		{"http://127.0.0.1:8080/", "ws://127.0.0.1:8080/api/plugins/kanban/events"},
	}
	for _, tt := range tests {
		w := NewKanbanWatcher(tt.input)
		got := w.buildWSURL()
		if got != tt.want {
			t.Errorf("buildWSURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestKanbanWatcher_BuildHTTPURL verifies URL conversion from WS to HTTP.
func TestKanbanWatcher_BuildHTTPURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"https://example.com", "https://example.com"},
		{"ws://127.0.0.1:9000", "http://127.0.0.1:9000"},
		{"wss://example.com", "https://example.com"},
	}
	for _, tt := range tests {
		w := NewKanbanWatcher(tt.input)
		got := w.buildHTTPURL()
		if got != tt.want {
			t.Errorf("buildHTTPURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestKanbanWatcher_TaskStatus_AfterSeed starts a mock HTTP server returning
// a board with tasks of varying statuses and verifies seedCounts populates
// the taskStatuses map correctly.
func TestKanbanWatcher_TaskStatus_AfterSeed(t *testing.T) {
	board := kanbanBoardResponse{
		Tasks: []kanbanTask{
			{ID: "t_abc123", Status: "running"},
			{ID: "t_def456", Status: "blocked"},
			{ID: "t_ghi789", Status: "completed"},
			{ID: "t_jkl012", Status: "claimed"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(board)
	}))
	defer srv.Close()

	watcher := NewKanbanWatcher(srv.URL)
	running, blocked, statuses, err := watcher.seedCounts(context.Background())
	if err != nil {
		t.Fatalf("seedCounts: %v", err)
	}

	if running != 2 {
		t.Errorf("running = %d, want 2 (one running + one claimed)", running)
	}
	if blocked != 1 {
		t.Errorf("blocked = %d, want 1", blocked)
	}

	if got := statuses["t_abc123"]; got != "running" {
		t.Errorf("statuses[t_abc123] = %q, want %q", got, "running")
	}
	if got := statuses["t_jkl012"]; got != "running" {
		t.Errorf("statuses[t_jkl012] = %q, want %q (claimed maps to running)", got, "running")
	}
	if got := statuses["t_def456"]; got != "blocked" {
		t.Errorf("statuses[t_def456] = %q, want %q", got, "blocked")
	}
	if got := statuses["t_ghi789"]; got != "" {
		t.Errorf("statuses[t_ghi789] = %q, want %q (completed not tracked)", got, "")
	}

	// Verify TaskStatus returns correctly after setCountsAndStatusesAndNotify.
	watcher.setCountsAndStatusesAndNotify(running, blocked, statuses, false)
	if got := watcher.TaskStatus("t_abc123"); got != "running" {
		t.Errorf("TaskStatus(t_abc123) = %q, want %q", got, "running")
	}
	if got := watcher.TaskStatus("t_def456"); got != "blocked" {
		t.Errorf("TaskStatus(t_def456) = %q, want %q", got, "blocked")
	}
	if got := watcher.TaskStatus("t_ghi789"); got != "" {
		t.Errorf("TaskStatus(t_ghi789) = %q, want empty (terminal)", got)
	}
}

// TestKanbanWatcher_TaskStatus_AfterApplyEvent verifies that applyEvent correctly
// updates the per-task status map through claim → blocked → completed transitions.
func TestKanbanWatcher_TaskStatus_AfterApplyEvent(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")

	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "t_abc123"})
	if got := w.TaskStatus("t_abc123"); got != "running" {
		t.Errorf("after claimed: TaskStatus = %q, want %q", got, "running")
	}

	w.applyEvent(kanbanEvent{ID: 2, Kind: "blocked", TaskID: "t_abc123"})
	if got := w.TaskStatus("t_abc123"); got != "blocked" {
		t.Errorf("after blocked: TaskStatus = %q, want %q", got, "blocked")
	}

	w.applyEvent(kanbanEvent{ID: 3, Kind: "completed", TaskID: "t_abc123"})
	if got := w.TaskStatus("t_abc123"); got != "" {
		t.Errorf("after completed: TaskStatus = %q, want empty (terminal)", got)
	}
}

// TestKanbanWatcher_TaskStatus_Unblocked verifies that an "unblocked" event
// transitions a task from blocked back to running.
func TestKanbanWatcher_TaskStatus_Unblocked(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")

	w.applyEvent(kanbanEvent{ID: 1, Kind: "blocked", TaskID: "t_abc123"})
	if got := w.TaskStatus("t_abc123"); got != "blocked" {
		t.Errorf("after blocked: TaskStatus = %q, want %q", got, "blocked")
	}

	w.applyEvent(kanbanEvent{ID: 2, Kind: "unblocked", TaskID: "t_abc123"})
	if got := w.TaskStatus("t_abc123"); got != "running" {
		t.Errorf("after unblocked: TaskStatus = %q, want %q", got, "running")
	}
}

// TestKanbanWatcher_TaskStatus_UnknownTask verifies that TaskStatus returns ""
// for a task ID that has never been seen.
func TestKanbanWatcher_TaskStatus_UnknownTask(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	if got := w.TaskStatus("t_nonexistent"); got != "" {
		t.Errorf("TaskStatus(t_nonexistent) = %q, want empty", got)
	}
}

// TestKanbanWatcher_ApplyEvent_RunningToBlocked verifies that a running→blocked
// transition decrements running AND increments blocked (not just increments blocked).
// Without state reconciliation this test fails: running stays at 1, blocked hits 1.
func TestKanbanWatcher_ApplyEvent_RunningToBlocked(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "t_x"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "blocked", TaskID: "t_x"})
	running, blocked := w.Counts()
	if running != 0 || blocked != 1 {
		t.Errorf("after claimed+blocked: running=%d blocked=%d, want 0 1", running, blocked)
	}
}

// TestKanbanWatcher_ApplyEvent_BlockedToCompleted verifies that completing a blocked
// task decrements blocked (not running). Without reconciliation this decrements running.
func TestKanbanWatcher_ApplyEvent_BlockedToCompleted(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "t_x"})
	w.applyEvent(kanbanEvent{ID: 2, Kind: "blocked", TaskID: "t_x"})
	w.applyEvent(kanbanEvent{ID: 3, Kind: "completed", TaskID: "t_x"})
	running, blocked := w.Counts()
	if running != 0 || blocked != 0 {
		t.Errorf("after claimed+blocked+completed: running=%d blocked=%d, want 0 0", running, blocked)
	}
}

// TestKanbanWatcher_ApplyEvent_DoubleClaimed verifies that a duplicate claimed
// event for a tracked task does not double-count the running counter.
func TestKanbanWatcher_ApplyEvent_DoubleClaimed(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "t_x"})
	// Duplicate claimed with different ID (passed the ID dedup filter)
	w.applyEvent(kanbanEvent{ID: 2, Kind: "claimed", TaskID: "t_x"})
	running, _ := w.Counts()
	if running != 1 {
		t.Errorf("after double-claimed: running=%d, want 1 (no double-count)", running)
	}
}

// TestKanbanWatcher_StartIsIdempotent verifies that calling Start() multiple times
// does not launch extra goroutines (the reconnect loop must start exactly once).
// We stop the watcher immediately and verify no panic or deadlock occurs.
func TestKanbanWatcher_StartIsIdempotent(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")
	w.Stop() // pre-stop so reconnectLoop exits immediately when started
	w.Start()
	w.Start() // second call must be a no-op
	w.Start() // third call must be a no-op
	// If two goroutines were started, both would race to read stopCh.
	// This test primarily guards against panics; the sync.Once ensures correctness.
}

// TestKanbanWatcher_ApplyEvent_SkipsDuplicate verifies that replayed events
// with the same ID are ignored and do not increment counts twice.
// Before the fix, replaying an event with id=1 would double-count it.
func TestKanbanWatcher_ApplyEvent_SkipsDuplicate(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")

	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "t_abc"})
	running, _ := w.Counts()
	if running != 1 {
		t.Fatalf("after first claimed: running=%d, want 1", running)
	}

	// Replay the same event — must be a no-op.
	w.applyEvent(kanbanEvent{ID: 1, Kind: "claimed", TaskID: "t_abc"})
	running, _ = w.Counts()
	if running != 1 {
		t.Errorf("after replayed claimed (same ID): running=%d, want 1 (no double-count)", running)
	}
}

// TestKanbanWatcher_ApplyEvent_SkipsOlderID verifies that an event with a lower
// ID than lastEventID is dropped, preventing out-of-order replay drift.
func TestKanbanWatcher_ApplyEvent_SkipsOlderID(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")

	w.applyEvent(kanbanEvent{ID: 5, Kind: "claimed", TaskID: "t_abc"})
	w.applyEvent(kanbanEvent{ID: 3, Kind: "claimed", TaskID: "t_def"}) // older — must be skipped
	running, _ := w.Counts()
	if running != 1 {
		t.Errorf("after older event replay: running=%d, want 1", running)
	}
}

// TestKanbanWatcher_ApplyEvent_ZeroIDNotSkipped verifies that events with ID=0
// (no sequence number) are always applied regardless of lastEventID.
func TestKanbanWatcher_ApplyEvent_ZeroIDNotSkipped(t *testing.T) {
	w := NewKanbanWatcher("http://127.0.0.1:0")

	w.applyEvent(kanbanEvent{ID: 5, Kind: "claimed", TaskID: "t_abc"})
	w.applyEvent(kanbanEvent{ID: 0, Kind: "claimed", TaskID: "t_def"}) // ID=0 means no sequencing
	running, _ := w.Counts()
	if running != 2 {
		t.Errorf("after ID=0 event: running=%d, want 2", running)
	}
}
