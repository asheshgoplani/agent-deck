package voicedesk

// Adversarial cases from the independent review of the voice desk (RV1-RV9).
// Each test states the behaviour the contract asks for; a FAIL is a finding.

import (
	"encoding/json"
	"strings"
	"testing"
)

// RV1. Number rule: a spoken number that is not the title's number is never
// that session ("0995", "09955", "095" are not "0955").
func TestReviewNumberRuleWithDoubledDigits(t *testing.T) {
	for _, n := range []string{"0995", "09955", "095", "00955"} {
		t.Run(n, func(t *testing.T) {
			h := newHarness(t)
			got := h.say("status of audit " + n)
			if targetID(got) == idAudit0955 {
				t.Fatalf("audit %s resolved to audit-0955: %q", n, got.Say)
			}
			h.do(Request{Sentence: "tell audit " + n + " to run the tests", On: idAudit0955})
			if len(h.deck.sent) != 0 {
				t.Fatalf("audit %s: sent to audit-0955 with no question: %+v", n, h.deck.sent)
			}
		})
	}
}

// RV2. Rule 6: when the read-back holds a secret, nothing of that text may be
// in show either.
func TestReviewSecretReadBackIsNotShown(t *testing.T) {
	h := newHarness(t)
	got := h.say("tell phone build that the password is tiger")
	b, _ := json.Marshal(got)
	if refusedOf(got) == RefusedUnsafe && strings.Contains(string(b), "tiger") {
		t.Fatalf("refused as a secret but the text is still in the result: %s", b)
	}
}

// RV3. A wrong kind of answer ("first" to a yes question) must not leave him
// with a line that asks for an answer nobody can give: either the question
// stays open (confirm present) or the line does not ask again.
func TestReviewWrongKindAnswerDoesNotAskIntoTheVoid(t *testing.T) {
	h := newHarness(t)
	ask := h.say("tell phone build to run the tests")
	got := h.do(Request{Sentence: "first", Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
	if strings.Contains(got.Say, "say yes or no") && got.Confirm == nil {
		// the caller has no token now: the next "yes" is typed into the session he is on
		next := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
		t.Fatalf("desk said %q with confirm=null; the following yes gives %q (sent=%d)", got.Say, next.Say, len(h.deck.sent))
	}
}

// RV4. A session that waits at an interactive menu (a permission question)
// must not get typed text plus Enter from a voice tell: Enter picks the
// highlighted "Yes".
func TestReviewNoBlindSendIntoAnOpenMenu(t *testing.T) {
	h := newHarness(t)
	for i := range h.deck.sessions {
		if h.deck.sessions[i].ID == idPhone {
			h.deck.sessions[i].Substate = "interactive-menu"
		}
	}
	ask := h.say("tell phone build to run the tests")
	if ask.Confirm != nil {
		h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
	}
	if len(h.deck.sent) != 0 {
		t.Fatalf("sent %q into a session whose substate is interactive-menu", h.deck.sent[0].msg)
	}
}

// RV5. Ordinary coding talk that begins with a desk verb and names no
// session goes on to the session he is on.
func TestReviewCodingTalkIsNotStolen(t *testing.T) {
	// Conductor ruling (after this review): an explicit tell to a NAME is
	// never handed on as plain text; it reads back, asks which, or answers
	// "not found". So the review's tell rows to unknown names ("tell git to
	// …", "tell the logger to …", "tell the notes app to …", "tell the
	// conductor class to …", "ask the notes module to …", "tell mac app users
	// to …", "बिल्ड को बोलो कि …") moved to TestTellIsNeverHandedOn. Rows with
	// a generic addressee ("यूजर" = the user) or no tell stay here.
	for _, s := range []string{
		"status of the pull request",
		"यूजर को बोलो कि ऐप रीस्टार्ट करे",
		"list all",
	} {
		t.Run(s, func(t *testing.T) {
			h := newHarness(t)
			got := h.do(Request{Sentence: s, On: idConductor, DryRun: true})
			if got.Handled {
				t.Fatalf("stolen: intent=%s refused=%s say=%q", got.Intent, refusedOf(got), got.Say)
			}
		})
	}
}

// RV6. Things a coding reply holds all the time are not secrets.
func TestReviewSecretFalsePositives(t *testing.T) {
	for _, s := range []string{
		"rename handleIncomingWebhookRequestV2WithRetries to something shorter",
		"the test TestStatusUpdatePass_DeadRowsStartNoTmuxPerRow_v2 is flaky",
		"image digest sha256:2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881",
		"checksum d41d8cd98f00b204e9800998ecf8427e matches",
		"the token is expired today",
		"auth is failing on staging",
		"the password is required for login",
		"the secret is stored in vault",
	} {
		if HasSecret(s) {
			t.Errorf("flagged as a secret: %q", s)
		}
	}
}

// RV7. Shapes the detector lets through.
func TestReviewSecretsMissed(t *testing.T) {
	for name, s := range map[string]string{
		"16-byte base64":               "b7ZdAKvDKvOOZn8CLoctSQ==",
		"40 hex (token, not a commit)": "api token ffc050fec94dbca3a0aac36098b2cc2bd8183194",
		"20 lower+digits":              "3f9caioctiq71hget7my",
		"24 capitals":                  "EBNWYZPSPACMQOOHZDHEEQVD",
		"otp":                          "the otp is 493817",
	} {
		if !HasSecret(s) {
			t.Errorf("%s not detected: %q", name, s)
		}
	}
}

// RV8. "yes?" is a question back ("yes? what?"), not consent.
func TestReviewQuestioningYesIsNotAYes(t *testing.T) {
	if k, _ := classifyAnswer("yes?"); k == AnswerYes {
		t.Errorf("\"yes?\" is taken as a yes")
	}
}

// RV9. The title said back after a yes is the session's title NOW (it was
// renamed between the question and the yes), or the send is refused.
func TestReviewRenamedTargetIsNotSaidWithTheOldName(t *testing.T) {
	h := newHarness(t)
	ask := h.say("tell phone build to run the tests")
	for i := range h.deck.sessions {
		if h.deck.sessions[i].ID == idPhone {
			h.deck.sessions[i].Title = "payments-prod"
		}
	}
	got := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
	if len(h.deck.sent) == 1 && strings.Contains(got.Say, "phone build") {
		t.Fatalf("sent to the session now called payments-prod, said %q", got.Say)
	}
}
