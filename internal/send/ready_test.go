package send

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

type mockReadyChecker struct {
	statuses []string
	statusIx atomic.Int64
	pane     string
}

func (m *mockReadyChecker) GetStatus() (string, error) {
	i := int(m.statusIx.Add(1)) - 1
	if i >= len(m.statuses) {
		return m.statuses[len(m.statuses)-1], nil
	}
	return m.statuses[i], nil
}

func (m *mockReadyChecker) CapturePaneFresh() (string, error) {
	return m.pane, nil
}

// Cursor can be interactive while GetStatus still returns "starting" during
// the tmux startup window (no activity-timestamp change → no prompt re-check).
func TestWaitForAgentReady_StartingWithCursorPrompt(t *testing.T) {
	mock := &mockReadyChecker{
		statuses: []string{"starting"},
		pane: strings.Join([]string{
			"Cursor Agent",
			"How can I help you today?",
			"› implement the feature",
			"Plan mode · Switch modes",
		}, "\n"),
	}

	err := WaitForAgentReady(mock, "cursor", 2*time.Second, PromptGates{})
	if err != nil {
		t.Fatalf("expected ready via startup prompt probe, got: %v", err)
	}
}

func TestWaitForAgentReady_StartingWithoutPromptTimesOut(t *testing.T) {
	mock := &mockReadyChecker{
		statuses: []string{"starting"},
		pane:     "Loading...\n",
	}

	err := WaitForAgentReady(mock, "cursor", 400*time.Millisecond, PromptGates{})
	if err == nil {
		t.Fatal("expected timeout when starting with no prompt")
	}
}

func TestWaitForAgentReady_CursorPromptDetector(t *testing.T) {
	content := "› ask anything\nPlan mode"
	d := tmux.NewPromptDetector("cursor")
	if !d.HasPrompt(content) {
		t.Fatalf("cursor detector should match › prompt, content:\n%s", content)
	}
}

type neverReadyChecker struct {
	calls atomic.Int64
}

func (m *neverReadyChecker) GetStatus() (string, error) {
	m.calls.Add(1)
	return "active", nil
}

func (m *neverReadyChecker) CapturePaneFresh() (string, error) {
	return "", nil
}

func TestWaitForAgentReady_RespectsTimeout(t *testing.T) {
	mock := &neverReadyChecker{}
	requested := 1 * time.Second
	start := time.Now()
	err := WaitForAgentReady(mock, "shell", requested, PromptGates{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error, got nil (elapsed=%v)", elapsed)
	}
	if elapsed > 3*requested {
		t.Fatalf("timeout ignored: elapsed=%v requested=%v", elapsed, requested)
	}
	lower := requested / 2
	if elapsed < lower {
		t.Fatalf("returned too quickly: elapsed=%v requested=%v lower=%v", elapsed, requested, lower)
	}
	if mock.calls.Load() == 0 {
		t.Error("expected GetStatus to be polled")
	}
}
