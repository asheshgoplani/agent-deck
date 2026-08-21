package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// marshalContext renders a document the way the CLI does, so these tests assert
// on the exact bytes a consumer receives.
func marshalContext(t *testing.T, doc interface{}) string {
	t.Helper()
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshalling the context document: %v", err)
	}
	return string(b)
}

func decodeContext(t *testing.T, doc interface{}) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(marshalContext(t, doc)), &out); err != nil {
		t.Fatalf("decoding the context document: %v", err)
	}
	return out
}

func TestBuildContextJSONCarriesIdentityAndSchema(t *testing.T) {
	v := contextTestView(t)
	got := decodeContext(t, buildContextJSON(v))

	if got["schema_version"] != float64(contextSchemaVersion) {
		t.Fatalf("schema_version = %v, want %d", got["schema_version"], contextSchemaVersion)
	}
	sess, ok := got["session"].(map[string]interface{})
	if !ok {
		t.Fatalf("session block missing: %v", got["session"])
	}
	for key, want := range map[string]string{
		"id":      "sess-abc",
		"title":   "my-project",
		"profile": "personal",
		"tool":    "claude",
		"path":    "/tmp/proj",
		"ref":     "my-project",
	} {
		if sess[key] != want {
			t.Fatalf("session.%s = %v, want %q", key, sess[key], want)
		}
	}
}

func TestBuildContextJSONTotalsMatchTheEngine(t *testing.T) {
	v := contextTestView(t)
	doc := buildContextJSON(v)

	fixed, complete := v.Report.FixedTotal()
	if doc.Totals.Fixed.Tokens != fixed || doc.Totals.Fixed.Complete != complete {
		t.Fatalf("totals.fixed = %+v, want %d/%v", doc.Totals.Fixed, fixed, complete)
	}
	attributed, attributedComplete := v.Report.AttributedTotal()
	if doc.Totals.Attributed.Tokens != attributed || doc.Totals.Attributed.Complete != attributedComplete {
		t.Fatalf("totals.attributed = %+v, want %d/%v", doc.Totals.Attributed, attributed, attributedComplete)
	}
	if doc.Totals.Potential == nil || doc.Totals.Potential.Tokens != 4_000 {
		t.Fatalf("totals.potential = %+v, want 4000", doc.Totals.Potential)
	}
	if doc.Totals.WindowPercent == nil || *doc.Totals.WindowPercent != 50 {
		t.Fatalf("totals.window_percent = %v, want 50", doc.Totals.WindowPercent)
	}
}

// Potential cost must never reach the gauge. It is the difference between "this
// costs you 100 tokens today" and "this could cost you 4,000 if invoked", and
// conflating them makes the user delete the wrong thing.
func TestBuildContextJSONKeepsPotentialOutOfTheFixedTotal(t *testing.T) {
	doc := buildContextJSON(contextTestView(t))
	if doc.Totals.Potential == nil {
		t.Fatal("fixture has no potential cost to check")
	}
	if doc.Totals.Fixed.Tokens == doc.Totals.Attributed.Tokens+doc.Totals.Potential.Tokens {
		t.Fatal("the fixed total appears to include potential cost")
	}
	if doc.Totals.Fixed.Tokens != 100_000 {
		t.Fatalf("totals.fixed = %d, want the measured anchor 100000", doc.Totals.Fixed.Tokens)
	}
}

// A percentage of an unknown denominator is the plausible-looking lie the
// feature exists to prevent, so the field is omitted rather than zeroed.
func TestBuildContextJSONOmitsWindowPercentWithoutAWindow(t *testing.T) {
	v := contextTestView(t)
	v.Report.Window = ctxinspect.WindowInfo{Source: ctxinspect.WindowUnknown}
	if got := marshalContext(t, buildContextJSON(v)); strings.Contains(got, "window_percent") {
		t.Fatalf("window_percent was emitted without a known window:\n%s", got)
	}
}

// The divide-by-zero trap: "tokens": 0 beside "source": "unknown" is honest
// only to a consumer that reads both fields, and the obvious thing to do with a
// window is used/window. Zero makes that +Inf or a crash, out of a document
// whose whole promise is that an unknown is never dressed up as a number.
func TestBuildContextJSONEncodesAnUnknownWindowAsNull(t *testing.T) {
	v := contextTestView(t)
	v.Report.Window = ctxinspect.WindowInfo{Source: ctxinspect.WindowUnknown, Detail: "no context-window size is known"}

	raw := marshalContext(t, buildContextJSON(v))
	if strings.Contains(raw, `"tokens": 0`) {
		t.Fatalf("an unknown window encoded a zero denominator:\n%s", raw)
	}
	if !strings.Contains(raw, `"source": "unknown"`) {
		t.Fatalf("an unknown window must still say so:\n%s", raw)
	}

	var doc struct {
		Report struct {
			Window struct {
				Tokens *int   `json:"tokens"`
				Source string `json:"source"`
			} `json:"window"`
		} `json:"report"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("decoding the document: %v", err)
	}
	if doc.Report.Window.Tokens != nil {
		t.Fatalf("window.tokens = %d, want null", *doc.Report.Window.Tokens)
	}
}

// An unknown token count encodes as null. A consumer must not be able to read a
// number that does not exist.
func TestBuildContextJSONEncodesUnknownTokensAsNull(t *testing.T) {
	v := contextTestView(t)
	v.Report.Categories[0].Items[0].Load.Actual = ctxinspect.UnknownTokens("this harness reports nothing")
	v.Report.Reconcile()
	_ = v.Report.Validate()

	raw := marshalContext(t, buildContextJSON(v))
	if !strings.Contains(raw, `"tokens": null`) {
		t.Fatalf("an unknown count did not encode as null:\n%s", raw)
	}

	doc := buildContextJSON(v)
	if doc.Totals.Attributed.Complete {
		t.Fatal("a total containing an unknown must not be marked complete")
	}
	if !strings.HasPrefix(doc.Totals.Attributed.Note, "LOWER BOUND") {
		t.Fatalf("an incomplete total must announce itself, got %q", doc.Totals.Attributed.Note)
	}
}

func TestBuildContextJSONReportsTokenAccountingSupport(t *testing.T) {
	supported := buildContextJSON(contextTestView(t))
	if !supported.TokenAccounting.Supported || supported.TokenAccounting.Reason != "" {
		t.Fatalf("token_accounting = %+v, want supported with no reason", supported.TokenAccounting)
	}

	v := contextTestView(t)
	v.Report.Capabilities = ctxinspect.Capabilities{
		Adapter:    "generic",
		Categories: []ctxinspect.CategoryCapability{{Name: "instruction-files", Token: ctxinspect.TokenUnknown}},
	}
	unsupported := buildContextJSON(v)
	if unsupported.TokenAccounting.Supported {
		t.Fatal("token_accounting.supported must be false for a harness with no accounting")
	}
	if strings.TrimSpace(unsupported.TokenAccounting.Reason) == "" {
		t.Fatal("token_accounting must explain why it is unsupported")
	}
}

// The document must be byte-stable for a given report: golden fixtures are the
// drift alarm for harness format changes, and a document that reorders itself
// between runs cannot be one.
func TestBuildContextJSONIsByteStable(t *testing.T) {
	v := contextTestView(t)
	first := marshalContext(t, buildContextJSON(v))
	for i := 0; i < 5; i++ {
		if got := marshalContext(t, buildContextJSON(v)); got != first {
			t.Fatalf("document is not byte-stable across marshals (run %d)", i)
		}
	}
}

func TestBuildContextJSONPreservesProvenanceOnEveryFigure(t *testing.T) {
	raw := marshalContext(t, buildContextJSON(contextTestView(t)))
	for _, want := range []string{
		`"provenance": "provider-measured"`,
		`"provenance": "estimated"`,
		`"provenance": "residual"`,
		`"provenance": "reconstructed"`,
		`"provenance": "captured"`,
		`"provenance": "absent"`,
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("document is missing %s\n%s", want, raw)
		}
	}
}

func TestBuildContextItemJSON(t *testing.T) {
	v := contextTestView(t)
	ri, err := findContextItem(v.Report, "skill:dataviz")
	if err != nil {
		t.Fatalf("findContextItem: %v", err)
	}
	doc := buildContextItemJSON(v, ri)

	if doc.SchemaVersion != contextSchemaVersion {
		t.Fatalf("schema_version = %d", doc.SchemaVersion)
	}
	if doc.Category != "skills" || doc.Item.ID != "skill:dataviz" {
		t.Fatalf("item document points at %s/%s", doc.Category, doc.Item.ID)
	}
	if doc.Badge != ri.Item.Badge() {
		t.Fatalf("badge = %+v, want the item's own badge %+v", doc.Badge, ri.Item.Badge())
	}

	decoded := decodeContext(t, doc)
	item, ok := decoded["item"].(map[string]interface{})
	if !ok {
		t.Fatalf("item block missing")
	}
	load, ok := item["load"].(map[string]interface{})
	if !ok {
		t.Fatalf("load block missing")
	}
	if load["state"] != "on-demand" {
		t.Fatalf("load.state = %v, want on-demand", load["state"])
	}
	if _, hasPotential := load["potential"]; !hasPotential {
		t.Fatal("an on-demand item must publish its potential cost")
	}
}

func TestBuildContextCapabilitiesJSON(t *testing.T) {
	caps := ctxinspect.Capabilities{
		Adapter:   "generic",
		CanAnchor: false,
		Categories: []ctxinspect.CategoryCapability{
			{Name: "instruction-files", Title: "instruction files", Text: ctxinspect.TextReconstructed, Token: ctxinspect.TokenUnknown, Note: "walked from disk"},
		},
		Notes: []string{"no token figures"},
	}
	view := contextCapabilitiesView("my-project", "my-project", "personal", "cursor-agent")
	doc := buildContextCapabilitiesJSON(view, caps)

	if doc.SchemaVersion != contextSchemaVersion {
		t.Fatalf("schema_version = %d", doc.SchemaVersion)
	}
	if doc.Session.Tool != "cursor-agent" || doc.Session.Title != "my-project" {
		t.Fatalf("session block = %+v", doc.Session)
	}
	raw := marshalContext(t, doc)
	for _, want := range []string{`"can_anchor": false`, `"token": "unknown"`, `"text": "reconstructed"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("capabilities document is missing %s\n%s", want, raw)
		}
	}
}

// A capabilities document must be answerable with no report at all: that is
// what makes it renderable for a session that has never run.
func TestContextCapabilitiesViewToleratesNoReport(t *testing.T) {
	view := contextCapabilitiesView("ref", "title", "personal", "aider")
	if view.Report != nil {
		t.Fatal("the capabilities view must not fabricate a report")
	}
	doc := buildContextCapabilitiesJSON(view, ctxinspect.Capabilities{Adapter: "generic"})
	if doc.Session.ID != "" || doc.Session.Path != "" {
		t.Fatalf("session block invented identity without a report: %+v", doc.Session)
	}
}
