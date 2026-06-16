package genui

import (
	"context"
	"fmt"
	"strings"
)

// compose.go — the genui-1 increment: the spec-emitting "LLM" layer.
//
// In genui-0 the whole-UI specs were HAND-AUTHORED inert data. genui-1 lets the
// spec be GENERATED from a user intent ("show me what's blocked") + the in-scope
// data catalog. The generated spec is just another spec: it MUST pass the
// unchanged validator (validate.go) before it can reach the renderer. The
// composer is therefore an UNTRUSTED producer behind the trusted gate.
//
// Two design pillars:
//
//  1. A pluggable SpecComposer interface. The "LLM" is injected, never wired
//     in. A deterministic StubComposer (compose_stub.go) drives the whole
//     intent→validate→repair→render loop in CI with ZERO LLM calls; a real
//     SessionComposer (compose_session.go) routes the prompt to an existing
//     agent-deck-managed claude session (the fleet IS the compute — no billed
//     API key, honoring the hard no-`claude -p` constraint).
//
//  2. A bounded repair loop (ComposeView). If validate() rejects an emitted
//     spec, the validator's structured errors are fed back to the composer and
//     it retries, capped at maxTries. If it still fails, a clean error is
//     surfaced — the renderer NEVER receives unvalidated output.

// BindDoc documents one available data reference (a `bind` / `repeat.over`
// path) the composer may target. It is the machine-readable half of the
// LLM↔spec data contract; SystemPrompt renders it to text for a real LLM.
type BindDoc struct {
	Path string `json:"path"` // dotted ref, e.g. "totals.running"
	Kind string `json:"kind"` // "scalar" | "list" | "object"
	Desc string `json:"desc"` // one-line description
}

// Catalog is the CLOSED description the composer composes against: the layout
// primitives, the widget registry, and the available data refs. It carries no
// data values — only the shape of what a spec is allowed to reference. This is
// the same closed vocabulary the validator enforces, surfaced to the producer.
type Catalog struct {
	Schema     int       `json:"schema"`
	Primitives []string  `json:"primitives"`
	Widgets    []string  `json:"widgets"`
	Binds      []BindDoc `json:"binds"`
}

// DefaultCatalog is the catalog for the command-center fleet snapshot. The bind
// paths mirror GenuiPane.bindData — the secret-free projection of the live
// snapshot that the renderer resolves refs against. Keep these in lockstep:
// a bind the catalog advertises but the pane does not project resolves to
// undefined (renders "—" / empty), and a path the pane projects but the
// catalog omits is simply never suggested to the composer.
func DefaultCatalog() Catalog {
	return Catalog{
		Schema:     SchemaVersion,
		Primitives: sortedKeys(PrimitiveTypes),
		Widgets:    sortedKeys(WidgetTypes),
		Binds: []BindDoc{
			{Path: "totals.running", Kind: "scalar", Desc: "count of running sessions across the fleet"},
			{Path: "totals.waiting", Kind: "scalar", Desc: "count of sessions waiting on a human"},
			{Path: "totals.idle", Kind: "scalar", Desc: "count of idle sessions"},
			{Path: "conductors", Kind: "list", Desc: "the fleet: one per project; item fields: name, status, currentlyWorkingOn, counts.{running,waiting,idle}, sessions[]"},
			{Path: "sessions", Kind: "list", Desc: "every active session, flattened; item fields: id, title, status, workingOn"},
			{Path: "stuckSessions", Kind: "list", Desc: "sessions in error/stopped state; same item fields as sessions"},
			{Path: "decisionsWaiting", Kind: "list", Desc: "decisions waiting on a human; item fields: id, question"},
		},
	}
}

// ComposeRequest is the full input handed to a composer for ONE attempt. On a
// repair retry, PriorAttempt + PriorErrors carry the rejected spec and the
// validator's complaints so the composer can fix them rather than start over.
type ComposeRequest struct {
	Intent      string  // the user's natural-language intent
	Catalog     Catalog // the closed vocabulary + bind catalog
	DataSummary string  // compact, secret-free summary of in-scope data

	PriorAttempt []byte   // the previous (rejected) spec JSON; nil on first try
	PriorErrors  []string // validator errors from the previous attempt
}

// IsRepair reports whether this request is a repair retry.
func (r ComposeRequest) IsRepair() bool { return len(r.PriorErrors) > 0 }

// SpecComposer produces a spec JSON for an intent. Implementations are
// UNTRUSTED: their output is always re-validated by ComposeView before render.
type SpecComposer interface {
	// Name identifies the composer (for trace/telemetry, e.g. "stub").
	Name() string
	// Compose emits raw spec JSON for the request. It returns bytes (not a
	// parsed Spec) so the exact wire payload flows through the SAME
	// ValidateBytes gate the renderer trusts.
	Compose(ctx context.Context, req ComposeRequest) ([]byte, error)
}

// ComposeAttempt records one round of the repair loop for the trace surfaced to
// the client/operator. Raw is the emitted JSON; Errors are why it was rejected
// (empty on the accepted attempt).
type ComposeAttempt struct {
	Raw    string   `json:"raw"`
	Errors []string `json:"errors,omitempty"`
}

// ComposeResult is the outcome of a successful ComposeView.
type ComposeResult struct {
	Spec     *Spec            // the validated spec (safe to render)
	Raw      []byte           // the exact validated wire bytes
	Composer string           // which composer produced it
	Tries    int              // how many attempts it took (1 = first try)
	Repaired bool             // true if it took >1 try (a repair happened)
	Attempts []ComposeAttempt // per-attempt trace
}

// DefaultMaxTries bounds the repair loop: one initial attempt + up to two
// repairs. Past this, a clean error is surfaced rather than looping forever.
const DefaultMaxTries = 3

// ComposeError is returned when the composer cannot produce a valid spec within
// the try budget. It carries the per-attempt trace so the operator can see what
// the model emitted and why each was rejected.
type ComposeError struct {
	Intent   string
	Composer string
	Tries    int
	Attempts []ComposeAttempt
	Last     error // the final underlying error (validation or composer error)
}

func (e *ComposeError) Error() string {
	return fmt.Sprintf("composer %q could not produce a valid spec for intent %q after %d tries: %v",
		e.Composer, e.Intent, e.Tries, e.Last)
}

func (e *ComposeError) Unwrap() error { return e.Last }

// ComposeView runs the bounded intent→compose→validate→repair loop. It calls
// the composer, validates the emitted bytes with the UNCHANGED ValidateBytes
// gate, and on rejection feeds the structured errors back for a repair retry,
// up to maxTries. It returns a validated spec or a ComposeError carrying the
// full trace. The renderer only ever sees output that cleared this gate.
func ComposeView(ctx context.Context, composer SpecComposer, intent string, catalog Catalog, dataSummary string, maxTries int) (*ComposeResult, error) {
	if composer == nil {
		return nil, &ComposeError{Intent: intent, Composer: "<nil>", Last: fmt.Errorf("no composer configured")}
	}
	if maxTries < 1 {
		maxTries = DefaultMaxTries
	}

	req := ComposeRequest{Intent: intent, Catalog: catalog, DataSummary: dataSummary}
	attempts := make([]ComposeAttempt, 0, maxTries)
	var last error

	for try := 1; try <= maxTries; try++ {
		if err := ctx.Err(); err != nil {
			return nil, &ComposeError{Intent: intent, Composer: composer.Name(), Tries: try - 1, Attempts: attempts, Last: err}
		}

		raw, err := composer.Compose(ctx, req)
		if err != nil {
			// A composer-level failure (no JSON, transport error) is itself a
			// repairable condition: record it and retry with the message.
			last = err
			attempts = append(attempts, ComposeAttempt{Raw: string(raw), Errors: []string{err.Error()}})
			req.PriorAttempt = raw
			req.PriorErrors = []string{err.Error()}
			continue
		}

		spec, verr := ValidateBytes(raw)
		if verr == nil {
			attempts = append(attempts, ComposeAttempt{Raw: string(raw)})
			return &ComposeResult{
				Spec:     spec,
				Raw:      raw,
				Composer: composer.Name(),
				Tries:    try,
				Repaired: try > 1,
				Attempts: attempts,
			}, nil
		}

		// Rejected: record, feed the validator's complaint back, retry.
		last = verr
		attempts = append(attempts, ComposeAttempt{Raw: string(raw), Errors: []string{verr.Error()}})
		req.PriorAttempt = raw
		req.PriorErrors = []string{verr.Error()}
	}

	return nil, &ComposeError{
		Intent:   intent,
		Composer: composer.Name(),
		Tries:    maxTries,
		Attempts: attempts,
		Last:     last,
	}
}

// --- prompt construction (the LLM↔spec contract) --------------------------

// SystemPrompt renders the catalog into the system message for a real LLM: the
// schema, the closed widget registry + primitives, the exact field rules, the
// available bind catalog, and the hard "emit ONLY a JSON spec, no prose, no
// code fields" instruction. The StubComposer ignores this (it is deterministic)
// — it exists for the real SessionComposer dogfood path.
func (c Catalog) SystemPrompt() string {
	var b strings.Builder
	b.WriteString("You generate a DATA-ONLY UI view spec (JSON) for the agent-deck command center.\n")
	b.WriteString("Output RULES (a strict validator rejects anything else):\n")
	b.WriteString(fmt.Sprintf("- Emit ONE JSON object and NOTHING else. No prose, no markdown, no code fences. schema MUST be %d.\n", c.Schema))
	b.WriteString("- Top-level keys: schema, specId, title, version, root. specId is a short kebab id you choose.\n")
	b.WriteString("- A node has type + a few of: children, text, level (1-3), gap (sm|md|lg), cols (1-6), label, tone (neutral|ok|warn|danger|info), bind, when, repeat.\n")
	b.WriteString("- NO other keys are allowed anywhere. NEVER emit a field carrying code, HTML, a URL, a handler, src, script, or style — the spec is pure data.\n")
	b.WriteString("- Layout primitives (containers carry children): " + strings.Join(c.Primitives, ", ") + ".\n")
	b.WriteString("- Leaf widgets (carry a `bind` ref, NO children): " + strings.Join(c.Widgets, ", ") + ".\n")
	b.WriteString("- Data binds BY REFERENCE only. A bind/when/repeat.over value is a dotted identifier from this catalog; never a literal value or expression:\n")
	for _, bd := range c.Binds {
		b.WriteString(fmt.Sprintf("    %s (%s) — %s\n", bd.Path, bd.Kind, bd.Desc))
	}
	b.WriteString("- repeat renders a template once per list item: {\"repeat\":{\"over\":\"conductors\",\"as\":\"item\",\"template\":{...}}} then bind \"item.field\".\n")
	b.WriteString("Example: {\"schema\":1,\"specId\":\"blocked\",\"title\":\"Blocked\",\"version\":1,\"root\":{\"type\":\"col\",\"gap\":\"lg\",\"children\":[{\"type\":\"heading\",\"level\":1,\"text\":\"Blocked\"},{\"type\":\"decision-list\",\"bind\":\"decisionsWaiting\"}]}}\n")
	return b.String()
}

// UserMessage is the per-request message for a real LLM: the intent plus a
// compact, secret-free summary of the in-scope data so the model can choose
// widgets that have something to show. On a repair retry it appends the
// rejected attempt and the validator's errors with a fix instruction.
func (c Catalog) UserMessage(req ComposeRequest) string {
	var b strings.Builder
	b.WriteString("INTENT: " + strings.TrimSpace(req.Intent) + "\n")
	if s := strings.TrimSpace(req.DataSummary); s != "" {
		b.WriteString("IN-SCOPE DATA (summary): " + s + "\n")
	}
	if req.IsRepair() {
		b.WriteString("\nYour previous spec was REJECTED by the validator. Fix exactly these errors and re-emit the full JSON spec:\n")
		for _, e := range req.PriorErrors {
			b.WriteString("- " + e + "\n")
		}
		if len(req.PriorAttempt) > 0 {
			b.WriteString("Previous (rejected) spec:\n" + string(req.PriorAttempt) + "\n")
		}
	}
	b.WriteString("\nEmit ONLY the JSON spec.")
	return b.String()
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// simple insertion sort to avoid importing sort for a tiny slice
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
