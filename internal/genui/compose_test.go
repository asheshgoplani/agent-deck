package genui

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// compose_test.go — the genui-1 composer + repair-loop suite. Everything here
// runs with ZERO LLM: the StubComposer and a couple of test-only composers
// drive the full intent→compose→validate→repair loop deterministically. This is
// what keeps CI green without any API key or live session.

func TestStubComposer_AllCannedIntentsValidate(t *testing.T) {
	// Every canned intent must produce a spec that clears the UNCHANGED
	// validator — the stub is the reference producer for the real composer.
	intents := []struct {
		intent string
		specID string
	}{
		{"show me what's blocked", "composed-blocked"},
		{"group everything by project", "composed-by-project"},
		{"just the conductors and their last action", "composed-conductors"},
		{"all sessions", "composed-sessions"},
		{"how's it going", "composed-status"}, // default
	}
	for _, tc := range intents {
		res, err := ComposeView(context.Background(), StubComposer{}, tc.intent, DefaultCatalog(), "", DefaultMaxTries)
		if err != nil {
			t.Fatalf("intent %q: ComposeView failed: %v", tc.intent, err)
		}
		if res.Spec.SpecID != tc.specID {
			t.Errorf("intent %q: specId = %q, want %q", tc.intent, res.Spec.SpecID, tc.specID)
		}
		if res.Tries != 1 || res.Repaired {
			t.Errorf("intent %q: expected first-try success, got tries=%d repaired=%v", tc.intent, res.Tries, res.Repaired)
		}
		if res.Composer != "stub" {
			t.Errorf("intent %q: composer = %q", tc.intent, res.Composer)
		}
	}
}

func TestStubComposer_InvalidProbeIsRejectedCleanly(t *testing.T) {
	// The deliberate "unknown widget" probe must be REJECTED by the validator
	// across all tries (the stub re-emits the same invalid spec), and surface a
	// ComposeError carrying the trace — never a rendered spec. This proves the
	// security keystone holds on COMPOSED output.
	_, err := ComposeView(context.Background(), StubComposer{}, "please use an unknown widget", DefaultCatalog(), "", DefaultMaxTries)
	if err == nil {
		t.Fatal("expected ComposeView to fail on the invalid probe")
	}
	var ce *ComposeError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ComposeError, got %T: %v", err, err)
	}
	if ce.Tries != DefaultMaxTries {
		t.Errorf("expected %d tries, got %d", DefaultMaxTries, ce.Tries)
	}
	if len(ce.Attempts) != DefaultMaxTries {
		t.Errorf("expected %d attempts in trace, got %d", DefaultMaxTries, len(ce.Attempts))
	}
	// The validator's complaint must mention the unknown type.
	last := ce.Attempts[len(ce.Attempts)-1]
	if len(last.Errors) == 0 || !strings.Contains(strings.Join(last.Errors, " "), "unknown type") {
		t.Errorf("expected an unknown-type validation error, got %v", last.Errors)
	}
}

// flakyComposer emits invalid specs for the first `failFor` tries, then a valid
// one. It also asserts the repair context is fed back: from the 2nd try on it
// must receive the prior attempt + the validator errors.
type flakyComposer struct {
	failFor   int
	calls     int
	sawRepair bool
	sawErrors []string
	validSpec string
	brokeSpec string
}

func (c *flakyComposer) Name() string { return "flaky" }

func (c *flakyComposer) Compose(_ context.Context, req ComposeRequest) ([]byte, error) {
	c.calls++
	if req.IsRepair() {
		c.sawRepair = true
		c.sawErrors = req.PriorErrors
		if len(req.PriorAttempt) == 0 {
			// Repair must include the prior attempt to fix.
			return []byte(c.brokeSpec), nil
		}
	}
	if c.calls <= c.failFor {
		return []byte(c.brokeSpec), nil
	}
	return []byte(c.validSpec), nil
}

func TestComposeView_RepairsAfterValidationError(t *testing.T) {
	fc := &flakyComposer{
		failFor:   1,
		brokeSpec: stubSpecInvalid, // unknown widget -> rejected
		validSpec: stubSpecBlocked, // valid
	}
	res, err := ComposeView(context.Background(), fc, "what's blocked", DefaultCatalog(), "12 running, 3 waiting", DefaultMaxTries)
	if err != nil {
		t.Fatalf("expected repair to succeed, got %v", err)
	}
	if !res.Repaired || res.Tries != 2 {
		t.Errorf("expected repaired on the 2nd try, got tries=%d repaired=%v", res.Tries, res.Repaired)
	}
	if !fc.sawRepair {
		t.Error("composer never received repair context")
	}
	if len(fc.sawErrors) == 0 || !strings.Contains(strings.Join(fc.sawErrors, " "), "unknown type") {
		t.Errorf("repair context missing validator errors, got %v", fc.sawErrors)
	}
	if res.Spec.SpecID != "composed-blocked" {
		t.Errorf("repaired spec id = %q", res.Spec.SpecID)
	}
}

func TestComposeView_ExhaustsTriesThenErrors(t *testing.T) {
	fc := &flakyComposer{failFor: 99, brokeSpec: stubSpecInvalid, validSpec: stubSpecBlocked}
	_, err := ComposeView(context.Background(), fc, "x", DefaultCatalog(), "", 2)
	if err == nil {
		t.Fatal("expected error after exhausting tries")
	}
	var ce *ComposeError
	if !errors.As(err, &ce) || ce.Tries != 2 {
		t.Fatalf("expected ComposeError with 2 tries, got %v", err)
	}
	if fc.calls != 2 {
		t.Errorf("composer should be called exactly maxTries(2) times, got %d", fc.calls)
	}
}

// errComposer always fails at the transport level (no bytes). ComposeView must
// treat it as a repairable condition and ultimately surface a ComposeError.
type errComposer struct{ calls int }

func (c *errComposer) Name() string { return "err" }
func (c *errComposer) Compose(_ context.Context, _ ComposeRequest) ([]byte, error) {
	c.calls++
	return nil, errors.New("transport boom")
}

func TestComposeView_ComposerErrorSurfacesCleanly(t *testing.T) {
	ec := &errComposer{}
	_, err := ComposeView(context.Background(), ec, "x", DefaultCatalog(), "", DefaultMaxTries)
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *ComposeError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ComposeError, got %T", err)
	}
	if ec.calls != DefaultMaxTries {
		t.Errorf("expected %d attempts, got %d", DefaultMaxTries, ec.calls)
	}
}

func TestComposeView_NilComposerErrors(t *testing.T) {
	if _, err := ComposeView(context.Background(), nil, "x", DefaultCatalog(), "", 3); err == nil {
		t.Fatal("expected error for nil composer")
	}
}

func TestComposeView_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ComposeView(ctx, StubComposer{}, "what's blocked", DefaultCatalog(), "", DefaultMaxTries)
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestCatalog_SystemPromptCoversContract(t *testing.T) {
	sp := DefaultCatalog().SystemPrompt()
	mustContain := []string{
		"DATA-ONLY", "schema", "decision-list", "status-list", "stat",
		"col", "row", "section", "totals.running", "conductors",
		"decisionsWaiting", "repeat", "code", // the "never emit code" rule
	}
	for _, s := range mustContain {
		if !strings.Contains(sp, s) {
			t.Errorf("system prompt missing %q", s)
		}
	}
}

func TestCatalog_UserMessageIncludesRepairContext(t *testing.T) {
	req := ComposeRequest{
		Intent:       "what's blocked",
		Catalog:      DefaultCatalog(),
		DataSummary:  "3 decisions waiting",
		PriorAttempt: []byte(`{"bad":true}`),
		PriorErrors:  []string{"$.root: root node is required"},
	}
	msg := req.Catalog.UserMessage(req)
	for _, s := range []string{"what's blocked", "3 decisions waiting", "REJECTED", "root node is required", `{"bad":true}`} {
		if !strings.Contains(msg, s) {
			t.Errorf("user message missing %q\n--- got ---\n%s", s, msg)
		}
	}
}

func TestDefaultCatalog_BindsAreSafeRefs(t *testing.T) {
	// Every advertised bind path must itself be a valid ref the validator
	// accepts — the catalog can't suggest a path the renderer would reject.
	for _, bd := range DefaultCatalog().Binds {
		if !isSafeRef(bd.Path) {
			t.Errorf("catalog bind %q is not a safe ref", bd.Path)
		}
	}
}

// --- SessionComposer (real path) prompt + extraction, no live session -----

func TestSessionComposer_BuildsPromptAndExtractsJSON(t *testing.T) {
	var gotPrompt string
	sc := &SessionComposer{
		Session: "conductor-agent-deck",
		Run: func(_ context.Context, sess, prompt string) (string, error) {
			if sess != "conductor-agent-deck" {
				t.Errorf("session = %q", sess)
			}
			gotPrompt = prompt
			// Simulate a chatty model: prose + a fenced JSON spec.
			return "Sure! Here's your view:\n```json\n" + stubSpecBlocked + "\n```\nHope that helps.", nil
		},
	}
	res, err := ComposeView(context.Background(), sc, "what's blocked", DefaultCatalog(), "3 waiting", DefaultMaxTries)
	if err != nil {
		t.Fatalf("ComposeView: %v", err)
	}
	if res.Spec.SpecID != "composed-blocked" {
		t.Errorf("specId = %q", res.Spec.SpecID)
	}
	if res.Composer != "session:conductor-agent-deck" {
		t.Errorf("composer name = %q", res.Composer)
	}
	// The prompt must carry the contract + the intent + the data summary.
	for _, s := range []string{"DATA-ONLY", "INTENT: what's blocked", "3 waiting"} {
		if !strings.Contains(gotPrompt, s) {
			t.Errorf("prompt missing %q", s)
		}
	}
}

func TestSessionComposer_NoSessionErrors(t *testing.T) {
	sc := &SessionComposer{}
	if _, err := sc.Compose(context.Background(), ComposeRequest{Intent: "x", Catalog: DefaultCatalog()}); err == nil {
		t.Fatal("expected error with no session configured")
	}
}

func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  string
		isErr bool
	}{
		{"plain", `{"a":1}`, `{"a":1}`, false},
		{"prose-wrapped", `here you go: {"a":1} done`, `{"a":1}`, false},
		{"fenced", "```json\n{\"a\":1}\n```", `{"a":1}`, false},
		{"fenced-no-lang", "```\n{\"a\":1}\n```", `{"a":1}`, false},
		{"brace-in-string", `{"q":"use } carefully","b":2}`, `{"q":"use } carefully","b":2}`, false},
		{"nested", `{"a":{"b":2},"c":3}`, `{"a":{"b":2},"c":3}`, false},
		{"escaped-quote", `{"q":"a \" } b"}`, `{"q":"a \" } b"}`, false},
		{"none", `no json here`, "", true},
		{"unbalanced", `{"a":1`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractJSONObject(tc.in)
			if tc.isErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
