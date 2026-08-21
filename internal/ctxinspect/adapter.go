package ctxinspect

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrUnsupported is returned when no adapter — not even the fallback — can
// report on a tool. Callers render "unsupported for <tool>" rather than a
// fabricated number.
var ErrUnsupported = errors.New("ctxinspect: context inspection unsupported for this tool")

// CategoryCapability declares, per category, the best provenance an adapter can
// achieve for a harness. It is static: it must be answerable without touching
// disk, so the UI can render a truthful "what this harness can tell you" screen
// before — or instead of — a successful inspection.
type CategoryCapability struct {
	// Name matches the [Category.Name] the adapter will emit.
	Name string `json:"name"`
	// Title is the display heading.
	Title string `json:"title"`
	// Text is the best text provenance achievable for this category.
	Text TextProv `json:"text"`
	// Token is the best token provenance achievable for this category.
	Token TokenProv `json:"token"`
	// Note explains the mechanism, or why the provenance is not better.
	Note string `json:"note,omitempty"`
}

// Capabilities is an adapter's honest-degradation contract. It is queried
// before [Adapter.Inspect] runs, and it is copied onto every [Report], so a
// failed inspection still tells the user what the harness could have told them.
type Capabilities struct {
	// Adapter names the adapter these capabilities describe.
	Adapter string `json:"adapter"`
	// CanAnchor reports whether a provider-measured fixed-prefix total is
	// obtainable for this harness.
	CanAnchor bool `json:"can_anchor"`
	// CanVerbatimSystem reports whether the harness's base system prompt can be
	// shown as text.
	CanVerbatimSystem bool `json:"can_verbatim_system"`
	// Categories declares what is achievable per category, in the order the
	// adapter emits them.
	Categories []CategoryCapability `json:"categories"`
	// Notes are free-form limitations printed verbatim.
	Notes []string `json:"notes,omitempty"`
}

// Category returns the declared capability for a category name.
func (c Capabilities) Category(name string) (CategoryCapability, bool) {
	for _, cc := range c.Categories {
		if cc.Name == name {
			return cc, true
		}
	}
	return CategoryCapability{}, false
}

// Request carries everything an adapter needs, as plain values.
//
// Every path is resolved by the caller, which is deliberate: transcript
// resolution must be collision-checked against live peer sessions (two
// instances sharing one harness session id must not both resolve a transcript,
// or the panel shows another session's context), and that check needs live
// session state this package must not import.
type Request struct {
	// Tool is the agent-deck tool name, e.g. "claude" or a custom [tools.*]
	// name wrapping one.
	Tool string
	// SessionID is the harness's own session identifier, when known.
	SessionID string
	// SessionRef is how the user names this session to the agent-deck CLI (its
	// title or id). It is interpolated into command levers, so a lever the user
	// copies runs against the right session. Empty yields a "<session>"
	// placeholder rather than a command that would target the wrong one.
	SessionRef string
	// ProjectPath is the session's working directory.
	ProjectPath string
	// ConfigDir is the harness config directory resolved for this specific
	// instance. agent-deck's per-session scratch HOMEs make the process-wide
	// default frequently wrong, so adapters must use this rather than
	// re-deriving one.
	ConfigDir string
	// TranscriptPath is the collision-checked path to the session's on-disk
	// transcript or rollout. Empty means none was resolved, which forces a
	// projected report rather than an observed one.
	TranscriptPath string
	// Model is the model identifier, when the caller knows it.
	Model string
	// Host supplies agent-deck's own knowledge of the session. Nil is
	// tolerated: [Request.HostOrNone] substitutes a probe that reports
	// everything as unavailable, with a reason, rather than panicking.
	Host Host
	// Estimator prices captured text. Zero value means [DefaultEstimator].
	Estimator Estimator
	// Now is the report timestamp. Zero means time.Now.
	Now time.Time
}

// HostOrNone returns the request's host, substituting the no-op probe when none
// was wired so an unwired build degrades to an honest "inventory unavailable"
// rather than a panic or a silently empty section.
func (r Request) HostOrNone() Host {
	if r.Host == nil {
		return NoHost()
	}
	return r.Host
}

// EstimatorOrDefault returns the request's estimator, defaulting when unset.
func (r Request) EstimatorOrDefault() Estimator {
	if len(r.Estimator.Divisors) == 0 && r.Estimator.Default.CharsPerToken == 0 {
		return DefaultEstimator()
	}
	return r.Estimator
}

// Timestamp returns the report timestamp, defaulting to now.
func (r Request) Timestamp() time.Time {
	if r.Now.IsZero() {
		return time.Now().UTC()
	}
	return r.Now
}

// Adapter reports the context inventory for one family of harnesses.
//
// The interface mirrors internal/costs.Parser (Name / CanParse / Parse), the
// repository's one established adapter registry, with Capabilities added
// because a context report — unlike a cost event — has to be able to say what
// it cannot know.
type Adapter interface {
	// Name identifies the adapter in reports and errors.
	Name() string
	// Supports reports whether this adapter handles the tool. Implementations
	// route through the host's compatibility predicates so custom [tools.*]
	// entries wrapping claude or codex inherit the right adapter.
	Supports(tool string, host Host) bool
	// Capabilities declares what the adapter can achieve, without touching
	// disk.
	Capabilities() Capabilities
	// Inspect produces the report. It returns an error on a genuine failure —
	// a parse error, an unreadable file — and never a silently zeroed report.
	Inspect(ctx context.Context, req Request) (*Report, error)
}

// Registry routes a tool to an adapter and finalises every report centrally.
//
// The fallback is held in its own field rather than appended to the slice, so
// it can neither be shadowed by registration order nor dropped by a caller who
// builds a registry from a partial list.
type Registry struct {
	adapters []Adapter
	fallback Adapter
}

// NewRegistry builds a registry. fallback may be nil, in which case an
// unmatched tool yields [ErrUnsupported]; passing [NewGenericAdapter] instead
// makes an unsupported harness a populated screen rather than an error.
func NewRegistry(fallback Adapter, adapters ...Adapter) *Registry {
	r := &Registry{fallback: fallback}
	for _, a := range adapters {
		r.Register(a)
	}
	return r
}

// Register appends an adapter. First match wins, so order is significant.
func (r *Registry) Register(a Adapter) {
	if a == nil {
		return
	}
	r.adapters = append(r.adapters, a)
}

// Adapters returns the registered adapters in match order, excluding the
// fallback.
func (r *Registry) Adapters() []Adapter {
	out := make([]Adapter, len(r.adapters))
	copy(out, r.adapters)
	return out
}

// Fallback returns the fallback adapter, or nil.
func (r *Registry) Fallback() Adapter { return r.fallback }

// Resolve returns the adapter that will handle a tool, or nil.
func (r *Registry) Resolve(tool string, host Host) Adapter {
	if host == nil {
		host = NoHost()
	}
	for _, a := range r.adapters {
		if a.Supports(tool, host) {
			return a
		}
	}
	return r.fallback
}

// Capabilities returns what will be achievable for a tool, without inspecting
// anything. It is the screen the UI can always render, including when
// inspection later fails.
func (r *Registry) Capabilities(tool string, host Host) (Capabilities, error) {
	a := r.Resolve(tool, host)
	if a == nil {
		return Capabilities{}, fmt.Errorf("%w: %s", ErrUnsupported, tool)
	}
	return a.Capabilities(), nil
}

// Inspect routes the request and finalises the resulting report.
//
// Finalisation is central on purpose: an adapter written later cannot
// substitute its own reconciliation or skip validation. Reconcile computes the
// residual, Validate records any invariant breach on the report itself, and the
// capabilities the adapter declared are attached whether or not inspection
// succeeded.
//
// A validation breach does not fail the call. It is a bug in the adapter or a
// change in the harness format, and the report carries it in
// [Report.Violations] plus a bug-severity caveat so the panel says the report
// contradicts itself instead of rendering it as trustworthy. A genuine
// inspection failure — an unreadable or unparsable source — is returned as an
// error and never as an empty report.
func (r *Registry) Inspect(ctx context.Context, req Request) (*Report, error) {
	if strings.TrimSpace(req.Tool) == "" {
		return nil, fmt.Errorf("%w: no tool given", ErrUnsupported)
	}
	host := req.HostOrNone()
	req.Host = host

	a := r.Resolve(req.Tool, host)
	if a == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, req.Tool)
	}

	caps := a.Capabilities()
	rep, err := a.Inspect(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ctxinspect: adapter %q failed on tool %q: %w", a.Name(), req.Tool, err)
	}
	if rep == nil {
		return nil, fmt.Errorf("ctxinspect: adapter %q returned no report and no error for tool %q", a.Name(), req.Tool)
	}

	rep.Adapter = a.Name()
	rep.Capabilities = caps
	if rep.Harness == "" {
		rep.Harness = req.Tool
	}
	if rep.GeneratedAt.IsZero() {
		rep.GeneratedAt = req.Timestamp()
	}
	rep.noteDiskDerivedFreshness()
	rep.Reconcile()
	if verr := rep.Validate(); verr != nil {
		rep.AddCaveat("report-invalid",
			"this report breaks its own invariants and must not be trusted: "+verr.Error(),
			SeverityBug)
	}
	return rep, nil
}
