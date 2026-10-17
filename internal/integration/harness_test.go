package integration

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// nonAlphanumRE matches characters that are not alphanumeric or dash.
// Uses dashes because tmux sanitizeName keeps only [a-zA-Z0-9-].
var nonAlphanumRE = regexp.MustCompile(`[^a-zA-Z0-9-]`)

// TmuxHarness manages tmux sessions for integration tests with automatic cleanup.
// Sessions are created with a test-unique prefix and torn down via t.Cleanup.
type TmuxHarness struct {
	t        *testing.T
	sessions []*session.Instance
	prefix   string
}

// NewTmuxHarness creates a harness that auto-cleans tmux sessions when the test ends.
// Skips the test if no tmux server is available.
func NewTmuxHarness(t *testing.T) *TmuxHarness {
	t.Helper()
	skipIfNoTmuxServer(t)

	h := &TmuxHarness{
		t:      t,
		prefix: fmt.Sprintf("inttest-%s-", sanitizeName(t.Name())),
	}
	t.Cleanup(h.cleanup)
	return h
}

// CreateSession creates a session.Instance with the harness prefix prepended to the title.
// The session is tracked for automatic cleanup.
func (h *TmuxHarness) CreateSession(title, projectPath string) *session.Instance {
	h.t.Helper()
	inst := session.NewInstance(h.prefix+title, projectPath)
	h.sessions = append(h.sessions, inst)
	return inst
}

// cleanup kills all tracked sessions in reverse order. Best-effort: errors are ignored.
func (h *TmuxHarness) cleanup() {
	for i := len(h.sessions) - 1; i >= 0; i-- {
		inst := h.sessions[i]
		if inst.Exists() {
			_ = inst.Kill()
		}
	}
}

// sanitizeName replaces slashes and non-alphanumeric characters with dashes
// for safe tmux session names. Matches tmux's own sanitization behavior.
func sanitizeName(name string) string {
	return nonAlphanumRE.ReplaceAllString(name, "-")
}
