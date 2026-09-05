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

// paneSequenceChecker replays a scripted series of pane captures, so a test can
// model a TUI that is still painting rather than one frozen on one frame.
type paneSequenceChecker struct {
	status   string
	panes    []string
	captures atomic.Int64
}

func (p *paneSequenceChecker) GetStatus() (string, error) { return p.status, nil }

func (p *paneSequenceChecker) CapturePaneFresh() (string, error) {
	i := int(p.captures.Add(1)) - 1
	if i >= len(p.panes) {
		return p.panes[len(p.panes)-1], nil
	}
	return p.panes[i], nil
}

const claudeComposerPane = "❯ \n  ⏵⏵ bypass permissions on"

// A composer that appears for one frame and is gone the next was a transient
// paint, not a tool waiting for input. Accepting it is how a launch message
// gets typed into a TUI that is still mounting and loses its leading bytes.
func TestWaitForAgentReady_StartupPromptMustPersist(t *testing.T) {
	mock := &paneSequenceChecker{
		status: "starting",
		panes: []string{
			claudeComposerPane,
			"Loading MCP servers...",
			claudeComposerPane,
			"Connecting...",
			"Loading...",
		},
	}

	err := WaitForAgentReady(mock, "claude", 1500*time.Millisecond,
		PromptGates{ClaudeComposer: true})
	if err == nil {
		t.Fatal("a prompt that keeps disappearing must not count as ready")
	}
}

// The bypass still fires - it just needs the prompt to still be there.
func TestWaitForAgentReady_StartupPromptAcceptedOncePersistent(t *testing.T) {
	mock := &paneSequenceChecker{status: "starting", panes: []string{claudeComposerPane}}

	if err := WaitForAgentReady(mock, "claude", 5*time.Second,
		PromptGates{ClaudeComposer: true}); err != nil {
		t.Fatalf("a steady composer should be ready, got: %v", err)
	}
	if got := mock.captures.Load(); got < startupPromptConfirmations {
		t.Fatalf("expected at least %d pane captures before ready, got %d",
			startupPromptConfirmations, got)
	}
}

// A TUI that paints, repaints, then settles is the normal case, not a failure:
// once the prompt holds for the full run of confirmations it is accepted.
func TestWaitForAgentReady_StartupPromptSettlesAfterRepaint(t *testing.T) {
	mock := &paneSequenceChecker{
		status: "starting",
		panes: []string{
			claudeComposerPane,
			"Loading MCP servers...",
			claudeComposerPane,
			claudeComposerPane,
			claudeComposerPane,
		},
	}

	if err := WaitForAgentReady(mock, "claude", 5*time.Second,
		PromptGates{ClaudeComposer: true}); err != nil {
		t.Fatalf("a composer that settles should be ready, got: %v", err)
	}
}
