package core

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Class says whether a command only reads state or changes it.
type Class string

const (
	// Query commands read state and never persist anything.
	Query Class = "query"
	// Mutate commands may change the store, tmux, or the event journal.
	Mutate Class = "mutate"
)

// Remote says whether a command may be run on behalf of a remote caller
// (remote exec today, the daemon socket later).
type Remote string

const (
	// RemoteAllow permits remote invocation.
	RemoteAllow Remote = "allow"
	// RemoteDeny keeps the command local-only.
	RemoteDeny Remote = "deny"
)

// ExecFunc is the untyped form of a command body. Use Typed to build one from
// a func over concrete input and output types.
type ExecFunc func(ctx context.Context, in any) (any, error)

// Def describes one command. In and Out are zero values of the input and
// output types (e.g. SessionStartIn{}); they drive schema reflection and the
// type check in Registry.Run.
type Def struct {
	ID      string
	CLI     []string
	Summary string
	Class   Class
	Remote  Remote
	In      any
	Out     any
	Exec    ExecFunc
}

// InType returns the reflected input type.
func (d Def) InType() reflect.Type { return reflect.TypeOf(d.In) }

// OutType returns the reflected output type.
func (d Def) OutType() reflect.Type { return reflect.TypeOf(d.Out) }

// CLIPath returns the CLI path joined by spaces ("session start").
func (d Def) CLIPath() string { return strings.Join(d.CLI, " ") }

// Typed adapts a typed command body to ExecFunc. The input may be passed as
// In or *In.
func Typed[In, Out any](fn func(ctx context.Context, in In) (Out, error)) ExecFunc {
	return func(ctx context.Context, in any) (any, error) {
		switch v := in.(type) {
		case In:
			return fn(ctx, v)
		case *In:
			if v == nil {
				var zero In
				return fn(ctx, zero)
			}
			return fn(ctx, *v)
		default:
			var zero In
			return nil, fmt.Errorf("core: input %T does not match %T", in, zero)
		}
	}
}

// Registry holds command definitions keyed by id and by CLI path.
type Registry struct {
	mu    sync.RWMutex
	byID  map[string]Def
	byCLI map[string]string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byID: map[string]Def{}, byCLI: map[string]string{}}
}

// Register adds a command. It rejects an incomplete Def, a duplicate id, and
// a CLI path already claimed by another command.
func (r *Registry) Register(d Def) error {
	if d.ID == "" {
		return fmt.Errorf("core: register: empty id")
	}
	if d.Exec == nil {
		return fmt.Errorf("core: register %s: nil Exec", d.ID)
	}
	if d.In == nil || d.Out == nil {
		return fmt.Errorf("core: register %s: In and Out must be set", d.ID)
	}
	switch d.Class {
	case Query, Mutate:
	default:
		return fmt.Errorf("core: register %s: invalid class %q", d.ID, d.Class)
	}
	switch d.Remote {
	case RemoteAllow, RemoteDeny:
	default:
		return fmt.Errorf("core: register %s: invalid remote policy %q", d.ID, d.Remote)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[d.ID]; dup {
		return fmt.Errorf("core: register %s: duplicate id", d.ID)
	}
	if len(d.CLI) > 0 {
		key := d.CLIPath()
		if owner, taken := r.byCLI[key]; taken {
			return fmt.Errorf("core: register %s: CLI path %q already used by %s", d.ID, key, owner)
		}
		r.byCLI[key] = d.ID
	}
	r.byID[d.ID] = d
	return nil
}

// MustRegister is Register that panics; for init-time wiring of built-ins.
func (r *Registry) MustRegister(d Def) {
	if err := r.Register(d); err != nil {
		panic(err)
	}
}

// Lookup returns the command with the given id.
func (r *Registry) Lookup(id string) (Def, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.byID[id]
	return d, ok
}

// LookupCLI returns the command reached by the given CLI path.
func (r *Registry) LookupCLI(path ...string) (Def, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byCLI[strings.Join(path, " ")]
	if !ok {
		return Def{}, false
	}
	return r.byID[id], true
}

// Defs returns every registered command sorted by id.
func (r *Registry) Defs() []Def {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Def, 0, len(r.byID))
	for _, d := range r.byID {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RemoteAllowed reports whether the command id may be run remotely. Unknown
// ids are denied.
func (r *Registry) RemoteAllowed(id string) bool {
	d, ok := r.Lookup(id)
	return ok && d.Remote == RemoteAllow
}

// Run executes the command id with the given input. The returned Result
// carries the output, any warnings the command recorded, and deferred work
// the caller must release with Result.Finish once it has rendered the answer.
func (r *Registry) Run(ctx context.Context, id string, in any) *Result {
	d, ok := r.Lookup(id)
	if !ok {
		return &Result{ID: id, Err: Errorf(CodeUnknownCommand, "unknown command %q", id)}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if t := reflect.TypeOf(in); t != d.InType() && t != reflect.PointerTo(d.InType()) {
		return &Result{ID: id, Err: Errorf(CodeInvalidInput, "command %s takes %s, got %v", id, d.InType(), t)}
	}
	rc := &runState{}
	out, err := d.Exec(withRunState(ctx, rc), in)
	return &Result{ID: id, Out: out, Err: err, Warnings: rc.warnings, after: rc.after}
}

// Result is one command execution.
type Result struct {
	ID       string
	Out      any
	Err      error
	Warnings []string
	after    []func()
}

// Finish runs the work the command deferred until after its answer was
// delivered (journal writes that must not delay the reply). Safe to call once;
// later calls are no-ops.
func (r *Result) Finish() {
	after := r.after
	r.after = nil
	for _, fn := range after {
		fn()
	}
}

// Invoke runs id on reg and returns the typed output. Deferred work is
// returned for the caller to run after rendering.
func Invoke[Out any](ctx context.Context, reg *Registry, id string, in any) (Out, *Result) {
	res := reg.Run(ctx, id, in)
	var zero Out
	if res.Err != nil {
		return zero, res
	}
	out, ok := res.Out.(Out)
	if !ok {
		res.Err = Errorf(CodeInternal, "command %s returned %T, want %T", id, res.Out, zero)
		return zero, res
	}
	return out, res
}
