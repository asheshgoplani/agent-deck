// Package logassert captures slog records during a test and offers
// assertion helpers so tests can verify "this code path emitted
// hook_overflow with dropped>0" without grepping stderr.
//
// Use Capture as the slog.Handler for a fresh logger; the production
// code under test should accept a *slog.Logger via dependency injection
// (or, where global, swap slog.SetDefault for the duration of the test).
//
//	cap := logassert.NewCapture()
//	logger := slog.New(cap)
//	doWork(logger)
//	cap.AssertContains(t, "watcher_overflow")
//	rec := cap.MustOne(t, "hook_status_updated")
//	require.Equal(t, "running", rec.String("status"))
//
// Records preserve attribute order via flattening — groups become
// dotted prefixes ("watcher.dropped") so tests can address a single
// attribute regardless of WithGroup nesting.
package logassert

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// TB is the subset of testing.TB used by Assert helpers. We avoid pulling
// in *testing.T so the package can be exercised under stub Ts in its own
// unit tests.
type TB interface {
	Errorf(format string, args ...any)
	Helper()
}

// Record is a captured slog entry with attributes flattened to a
// dotted-key map (groups become "g.k").
type Record struct {
	Level   slog.Level
	Message string
	Attrs   map[string]any
}

// String returns the attribute as a string (using fmt %v if not a string).
// Returns "" if the key is absent.
func (r Record) String(key string) string {
	v, ok := r.Attrs[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// Int returns the attribute as an int. Returns 0 if absent or not numeric.
func (r Record) Int(key string) int {
	v, ok := r.Attrs[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	case uint64:
		return int(n)
	}
	return 0
}

// Bool returns the attribute as a bool. Returns false if absent or non-bool.
func (r Record) Bool(key string) bool {
	v, ok := r.Attrs[key]
	if !ok {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// Capture is the user-facing handle. It owns the storage; child views
// returned by WithAttrs/WithGroup all forward records back to it.
type Capture struct {
	mu      sync.Mutex
	records []Record
}

// NewCapture returns a fresh Capture handler.
func NewCapture() *Capture {
	return &Capture{}
}

// Records returns a copy of the captured records.
func (c *Capture) Records() []Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Record, len(c.records))
	copy(out, c.records)
	return out
}

// Reset clears the recorded log.
func (c *Capture) Reset() {
	c.mu.Lock()
	c.records = nil
	c.mu.Unlock()
}

// WithMessage returns the subset of records whose Message equals msg.
func (c *Capture) WithMessage(msg string) []Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Record
	for _, r := range c.records {
		if r.Message == msg {
			out = append(out, r)
		}
	}
	return out
}

// AssertContains fails the test if no record has Message == msg.
func (c *Capture) AssertContains(t TB, msg string) {
	t.Helper()
	if len(c.WithMessage(msg)) == 0 {
		t.Errorf("logassert: expected message %q, got: %s", msg, c.summary())
	}
}

// AssertNotContains fails the test if any record has Message == msg.
func (c *Capture) AssertNotContains(t TB, msg string) {
	t.Helper()
	if hits := c.WithMessage(msg); len(hits) > 0 {
		t.Errorf("logassert: unexpected message %q (%d occurrences)", msg, len(hits))
	}
}

// MustOne returns the single record with Message == msg, failing the test
// if zero or multiple match.
func (c *Capture) MustOne(t TB, msg string) Record {
	t.Helper()
	hits := c.WithMessage(msg)
	if len(hits) == 0 {
		t.Errorf("logassert: expected exactly one %q, got 0; have: %s", msg, c.summary())
		return Record{}
	}
	if len(hits) > 1 {
		t.Errorf("logassert: expected exactly one %q, got %d", msg, len(hits))
	}
	return hits[0]
}

// summary returns a one-line compact list of recorded messages, useful
// for failure diagnostics. Caller must NOT hold c.mu.
func (c *Capture) summary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	msgs := make([]string, 0, len(c.records))
	for _, r := range c.records {
		msgs = append(msgs, r.Message)
	}
	return "[" + strings.Join(msgs, ", ") + "]"
}

// append appends rec to the underlying record store.
func (c *Capture) append(rec Record) {
	c.mu.Lock()
	c.records = append(c.records, rec)
	c.mu.Unlock()
}

// Capture itself satisfies slog.Handler by delegating to a root view.
// This keeps the API ergonomic: slog.New(logassert.NewCapture()) works.

// Enabled reports whether the handler handles records at lvl. Always true.
func (c *Capture) Enabled(_ context.Context, _ slog.Level) bool { return true }

// Handle stores the record (as if from a root view with no group / attrs).
func (c *Capture) Handle(ctx context.Context, r slog.Record) error {
	return rootView(c).Handle(ctx, r)
}

// WithAttrs returns a child handler that prepends attrs to every record.
func (c *Capture) WithAttrs(attrs []slog.Attr) slog.Handler {
	return rootView(c).WithAttrs(attrs)
}

// WithGroup returns a child handler that nests subsequent attrs under name.
func (c *Capture) WithGroup(name string) slog.Handler {
	return rootView(c).WithGroup(name)
}

// view is an immutable handler instance carrying the inherited group
// prefix and With-attrs. It forwards records back to the owning Capture.
// Separating view from Capture avoids copying a sync.Mutex on each
// WithAttrs / WithGroup call.
type view struct {
	root        *Capture
	groupPrefix string
	withAttrs   []slog.Attr
}

func rootView(c *Capture) *view { return &view{root: c} }

func (v *view) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (v *view) Handle(_ context.Context, r slog.Record) error {
	rec := Record{
		Level:   r.Level,
		Message: r.Message,
		Attrs:   make(map[string]any),
	}
	for _, a := range v.withAttrs {
		flatten(rec.Attrs, v.groupPrefix, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		flatten(rec.Attrs, v.groupPrefix, a)
		return true
	})
	v.root.append(rec)
	return nil
}

func (v *view) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(v.withAttrs)+len(attrs))
	merged = append(merged, v.withAttrs...)
	merged = append(merged, attrs...)
	return &view{root: v.root, groupPrefix: v.groupPrefix, withAttrs: merged}
}

func (v *view) WithGroup(name string) slog.Handler {
	if name == "" {
		return v
	}
	prefix := name
	if v.groupPrefix != "" {
		prefix = v.groupPrefix + "." + name
	}
	return &view{root: v.root, groupPrefix: prefix, withAttrs: v.withAttrs}
}

// flatten inserts the slog.Attr into out, prepending the dotted prefix
// for groups. Nested groups recurse.
func flatten(out map[string]any, prefix string, a slog.Attr) {
	key := a.Key
	if prefix != "" {
		key = prefix + "." + a.Key
	}

	val := a.Value
	if val.Kind() == slog.KindGroup {
		for _, sub := range val.Group() {
			flatten(out, key, sub)
		}
		return
	}
	out[key] = val.Any()
}
