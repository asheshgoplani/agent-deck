package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxtext"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2026 (b): the proactive conductor /clear is the one destructive
// consumer of the context percentage. It must fail closed whenever the
// window it divides by is not knowable — inferred from the model id, unknown,
// or disproved by observed usage — because a conductor cleared on a guess
// loses its context, while one left to Claude's own compaction loses only an
// optimisation.

func TestClearOnCompactDue_InferredWindowStaysDisarmed(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "")
	// 90% of the table's 1M window, which used to arm the /clear. The id
	// has been seen on 200k sessions too, so the figure is a guess.
	a := &session.SessionAnalytics{Model: "claude-opus-5", CurrentContextTokens: 900000, PeakContextTokens: 900000}
	due, reason := clearOnCompactDue(a)
	if due {
		t.Fatalf("inferred window must not arm the /clear; reason=%q", reason)
	}
	if !strings.Contains(reason, "inferred") {
		t.Errorf("reason must say the window is inferred, got %q", reason)
	}
}

func TestClearOnCompactDue_OverLimitStaysDisarmed(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "")
	a := &session.SessionAnalytics{Model: "claude-opus-4-20250514", CurrentContextTokens: 494561, PeakContextTokens: 494561}
	if due, reason := clearOnCompactDue(a); due {
		t.Fatalf("a disproved window must not arm the /clear; reason=%q", reason)
	}
}

func TestClearOnCompactDue_UnknownModelStaysDisarmed(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "")
	a := &session.SessionAnalytics{Model: "never-seen-model", CurrentContextTokens: 199000, PeakContextTokens: 199000}
	if due, reason := clearOnCompactDue(a); due {
		t.Fatalf("unknown window must not arm the /clear; reason=%q", reason)
	}
}

func TestClearOnCompactDue_ConfiguredWindowArmsAtThreshold(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "200000")
	below := &session.SessionAnalytics{Model: "claude-opus-5", CurrentContextTokens: 150000, PeakContextTokens: 150000}
	if due, _ := clearOnCompactDue(below); due {
		t.Fatalf("75%% of a configured window must not be due")
	}
	above := &session.SessionAnalytics{Model: "claude-opus-5", CurrentContextTokens: 170000, PeakContextTokens: 170000}
	if due, reason := clearOnCompactDue(above); !due {
		t.Fatalf("85%% of a configured window must be due; reason=%q", reason)
	}
}

func TestClearOnCompactDue_NilAnalytics(t *testing.T) {
	if due, _ := clearOnCompactDue(nil); due {
		t.Fatal("nil analytics must not be due")
	}
}
