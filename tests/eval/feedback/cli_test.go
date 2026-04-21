//go:build eval_smoke

// Package feedback holds behavioral eval cases for the feedback CLI and TUI
// surfaces. See docs/rfc/EVALUATOR_HARNESS.md (issue #37) for the three
// shipped-but-unit-test-invisible bugs these cases exist to prevent.
package feedback_test

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestEval_FeedbackCLI_DisclosureBeforeConsent is the guard for RFC Bug 1
// (v1.7.35 shipped with renderFeedbackDisclosure buffered into a
// strings.Builder that flushed only after handleFeedback returned; users
// typed Rating, Comment, and the confirm answer against a blank cursor).
//
// Under a real PTY the bug re-appears structurally: if any layer between
// handleFeedback's fmt.Fprint calls and os.Stdout is a buffered wrapper, the
// disclosure and the "[y/N]:" prompt arrive together at flush time — AFTER
// the stdin read blocks. ExpectOutputBefore will then time out because
// neither token ever arrives before the harness closes the session.
//
// On fixed code both strings are written unbuffered, disclosure first, then
// the prompt — so ExpectOutputBefore succeeds.
func TestEval_FeedbackCLI_DisclosureBeforeConsent(t *testing.T) {
	sb := harness.NewSandbox(t)
	sb.GhShim.ScriptSuccess() // ghUserLogin() would succeed, but we decline

	p := sb.Spawn("feedback")
	defer p.Close()

	// Rating prompt is the first interactive point.
	p.ExpectOutput("Rating (1-5", 3*time.Second)
	p.Send("5\n")

	// Comment prompt arrives after rating is persisted.
	p.ExpectOutput("Comment", 3*time.Second)
	p.Send("eval harness smoke test\n")

	// The class-of-bug assertion: under a PTY the disclosure text MUST
	// reach stdout before the consent prompt blocks on stdin. If a future
	// change re-wraps stdout in a buffered writer, both tokens get
	// delayed past the next read and this assertion times out.
	p.ExpectOutputBefore("posted PUBLICLY", "[y/N]:", 3*time.Second)

	// Decline. Per v1.7.38 the CLI treats N at the disclosure as a
	// persistent opt-out, so the shim's gh graphql mutation MUST NOT be
	// called. This also covers RFC assertion "Decline does not call gh".
	p.Send("N\n")
	p.ExpectExit(0, 5*time.Second)

	sb.GhShim.AssertNotCalled("graphql")
}

// TestEval_FeedbackCLI_AcceptCallsGhGraphql is the other half of the CLI
// consent guarantee — that a `y` at the disclosure step ends in a gh api
// graphql mutation with the posting body, no clipboard/browser fallback.
func TestEval_FeedbackCLI_AcceptCallsGhGraphql(t *testing.T) {
	sb := harness.NewSandbox(t)
	sb.GhShim.ScriptSuccess()

	p := sb.Spawn("feedback")
	defer p.Close()

	p.ExpectOutput("Rating (1-5", 3*time.Second)
	p.Send("5\n")
	p.ExpectOutput("Comment", 3*time.Second)
	p.Send("accept path\n")
	p.ExpectOutput("Post this?", 3*time.Second)
	p.Send("y\n")
	p.ExpectExit(0, 5*time.Second)

	// The mutation body carries the rating and the comment text.
	call := sb.GhShim.AssertCalled("graphql")
	joined := ""
	for _, a := range call.Args {
		joined += a + " "
	}
	if !containsAny(joined, "addDiscussionComment") {
		t.Fatalf("gh graphql call missing mutation name: %v", call.Args)
	}
}

func containsAny(hay string, needles ...string) bool {
	for _, n := range needles {
		if len(n) == 0 {
			continue
		}
		for i := 0; i+len(n) <= len(hay); i++ {
			if hay[i:i+len(n)] == n {
				return true
			}
		}
	}
	return false
}
