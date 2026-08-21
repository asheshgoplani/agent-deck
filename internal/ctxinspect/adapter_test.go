package ctxinspect

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubAdapter is a minimal adapter used to exercise routing and finalisation.
type stubAdapter struct {
	name     string
	tools    []string
	report   *Report
	err      error
	nilBoth  bool
	inspects int
}

func (s *stubAdapter) Name() string { return s.name }

func (s *stubAdapter) Supports(tool string, _ Host) bool { return containsString(s.tools, tool) }

func (s *stubAdapter) Capabilities() Capabilities {
	return Capabilities{Adapter: s.name, CanAnchor: true}
}

func (s *stubAdapter) Inspect(context.Context, Request) (*Report, error) {
	s.inspects++
	if s.nilBoth {
		return nil, nil
	}
	if s.err != nil {
		return nil, s.err
	}
	// Return a fresh copy so a test cannot accidentally observe mutation from
	// an earlier call.
	cp := *s.report
	return &cp, nil
}

func newStub(name string, tools ...string) *stubAdapter {
	return &stubAdapter{
		name:  name,
		tools: tools,
		report: &Report{
			Harness: "",
			Basis:   BasisObserved,
			Anchor:  &Anchor{Tokens: Measured(1000, "usage"), Source: "usage"},
			Categories: []Category{{
				Name:  "system",
				Title: "system",
				Items: []Item{pricedItem("a", 400)},
			}},
		},
	}
}

func TestRegistryFirstMatchWins(t *testing.T) {
	first := newStub("first", "claude")
	second := newStub("second", "claude")
	r := NewRegistry(nil, first, second)

	if got := r.Resolve("claude", NoHost()); got != Adapter(first) {
		t.Fatalf("resolved %v, want the first registered adapter", got.Name())
	}
}

func TestRegistryFallbackCannotBeShadowed(t *testing.T) {
	fallback := newStub("fallback", "anything")
	specific := newStub("claude", "claude")
	r := NewRegistry(fallback, specific)

	if got := r.Resolve("claude", NoHost()); got.Name() != "claude" {
		t.Fatalf("resolved %q, want the specific adapter", got.Name())
	}
	if got := r.Resolve("cursor", NoHost()); got.Name() != "fallback" {
		t.Fatalf("resolved %q, want the fallback", got.Name())
	}
	if len(r.Adapters()) != 1 {
		t.Fatal("the fallback must be held separately from the ordered list so registration order cannot shadow it")
	}
	if r.Fallback().Name() != "fallback" {
		t.Fatal("the fallback must be retrievable")
	}
}

func TestRegistryWithoutAFallbackReportsUnsupported(t *testing.T) {
	r := NewRegistry(nil, newStub("claude", "claude"))
	_, err := r.Inspect(context.Background(), Request{Tool: "aider"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported so the UI prints \"unsupported\" instead of a fabricated number", err)
	}
	if _, err := r.Capabilities("aider", NoHost()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("capabilities error = %v, want ErrUnsupported", err)
	}
}

func TestRegistryRejectsAnEmptyTool(t *testing.T) {
	r := NewRegistry(NewGenericAdapter())
	if _, err := r.Inspect(context.Background(), Request{Tool: "  "}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported for a nameless tool", err)
	}
}

func TestRegistryFinalisesEveryReportCentrally(t *testing.T) {
	stub := newStub("claude", "claude")
	r := NewRegistry(nil, stub)

	rep, err := r.Inspect(context.Background(), Request{Tool: "claude"})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if rep.Adapter != "claude" {
		t.Fatalf("adapter = %q, want it stamped by the registry", rep.Adapter)
	}
	if rep.Harness != "claude" {
		t.Fatalf("harness = %q, want it defaulted from the request", rep.Harness)
	}
	if rep.Capabilities.Adapter != "claude" {
		t.Fatal("declared capabilities must be attached whether or not inspection succeeded")
	}
	if rep.GeneratedAt.IsZero() {
		t.Fatal("the registry must stamp a timestamp")
	}
	// An adapter cannot substitute its own reconciliation: the registry runs it.
	if rep.Unaccounted == nil {
		t.Fatal("the registry must reconcile every report")
	}
	if got, _ := rep.Reconciliation.Unaccounted.Value(); got != 600 {
		t.Fatalf("residual = %d, want 600", got)
	}
}

func TestRegistrySurfacesAdapterFailuresAsErrors(t *testing.T) {
	stub := newStub("claude", "claude")
	stub.err = errors.New("transcript is not valid jsonl")
	r := NewRegistry(nil, stub)

	rep, err := r.Inspect(context.Background(), Request{Tool: "claude"})
	if err == nil {
		t.Fatal("a parse failure must surface as an error, never as a silently zeroed report")
	}
	if rep != nil {
		t.Fatal("no report may be returned alongside an inspection failure")
	}
	if !strings.Contains(err.Error(), "not valid jsonl") {
		t.Fatalf("error %q must carry the adapter's own message", err)
	}
}

func TestRegistryRejectsAnAdapterThatReturnsNothing(t *testing.T) {
	stub := newStub("claude", "claude")
	stub.nilBoth = true
	r := NewRegistry(nil, stub)

	if _, err := r.Inspect(context.Background(), Request{Tool: "claude"}); err == nil {
		t.Fatal("an adapter returning neither a report nor an error is a bug and must not become an empty screen")
	}
}

func TestRegistryRecordsValidationBreachesWithoutDiscardingTheReport(t *testing.T) {
	stub := newStub("claude", "claude")
	bad := pricedItem("bad", 100)
	bad.Origin = OriginHarnessBuiltin // actionable lever on harness internals
	stub.report.Categories[0].Items = append(stub.report.Categories[0].Items, bad)
	r := NewRegistry(nil, stub)

	rep, err := r.Inspect(context.Background(), Request{Tool: "claude"})
	if err != nil {
		t.Fatalf("a validation breach is a bug to report, not a call to fail: %v", err)
	}
	if len(rep.Violations) == 0 {
		t.Fatal("violations must be recorded on the report")
	}
	var flagged bool
	for _, c := range rep.Caveats {
		if c.Code == "report-invalid" && c.Severity == SeverityBug {
			flagged = true
		}
	}
	if !flagged {
		t.Fatal("a self-contradicting report must say so on screen rather than render as trustworthy")
	}
}

func TestRegistryRoutesThroughHostCompatibility(t *testing.T) {
	// A custom [tools.*] entry wrapping claude must reach the Claude adapter.
	claudeish := &stubAdapter{
		name:  "claude",
		tools: nil,
		report: &Report{Basis: BasisProjected, Categories: []Category{{
			Name: "x", Title: "x", Items: []Item{pricedItem("a", 1)},
		}}},
	}
	// Route on compatibility rather than exact name.
	compat := &compatAdapter{stubAdapter: claudeish}
	host := &StaticHost{ClaudeTools: []string{"claude-yolo"}}
	r := NewRegistry(NewGenericAdapter(), compat)

	if got := r.Resolve("claude-yolo", host); got.Name() != "claude" {
		t.Fatalf("resolved %q, want the Claude adapter via the host's compatibility predicate", got.Name())
	}
	if got := r.Resolve("claude-yolo", NoHost()); got.Name() != "generic" {
		t.Fatalf("resolved %q with no host wired, want the generic fallback: an unwired build must under-report rather than route to the wrong adapter", got.Name())
	}
}

// compatAdapter routes by the host's compatibility predicate, the way a real
// harness adapter does.
type compatAdapter struct{ *stubAdapter }

func (c *compatAdapter) Supports(tool string, host Host) bool { return host.IsClaudeCompatible(tool) }

func TestRequestDefaults(t *testing.T) {
	var req Request
	if req.HostOrNone() == nil {
		t.Fatal("a nil host must be substituted, not dereferenced")
	}
	if req.HostOrNone().Name() != "none" {
		t.Fatal("the substituted host must identify itself so notes can name it")
	}
	if req.EstimatorOrDefault().Name == "" {
		t.Fatal("an unset estimator must default rather than divide by zero")
	}
	if req.Timestamp().IsZero() {
		t.Fatal("a zero Now must default to the current time")
	}
	custom := Request{Estimator: Estimator{Name: "custom", Default: KindDivisor{CharsPerToken: 3}}}
	if custom.EstimatorOrDefault().Name != "custom" {
		t.Fatal("a caller-supplied estimator must be honoured")
	}
}

func TestCapabilitiesLookup(t *testing.T) {
	caps := NewGenericAdapter().Capabilities()
	cc, ok := caps.Category(CategoryMCP)
	if !ok {
		t.Fatal("the generic adapter declares an MCP category")
	}
	if cc.Token != TokenUnknown {
		t.Fatalf("declared token provenance = %s, want unknown", cc.Token)
	}
	if _, ok := caps.Category("skills"); ok {
		t.Fatal("the generic adapter must not declare a category it cannot report")
	}
}
