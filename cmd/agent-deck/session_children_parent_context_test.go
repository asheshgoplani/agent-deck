package main

import "testing"

// The supervising conductor in an orchestrate run is the one session nobody
// else rotates. `session children` reports every child's context size; these
// tests pin the parent's own size onto the same output so a long run can see
// itself approach its handoff threshold instead of drifting past it.
func TestParentContextFieldsEmitsWhenResolved(t *testing.T) {
	suffix, emit := parentContextFields(183_400, true)
	if !emit {
		t.Fatalf("expected parent context to be emitted for a resolved size")
	}
	if suffix != "  self-ctx=183k" {
		t.Fatalf("unexpected human suffix: %q", suffix)
	}
}

func TestParentContextFieldsOmitsUnknown(t *testing.T) {
	// A zero must never reach the JSON: a supervisor thresholding on
	// parent_context_tokens would read it as "context is empty" and skip the
	// rotation the field exists to trigger.
	cases := []struct {
		name   string
		tokens int
		ok     bool
	}{
		{"unresolvable transcript", 0, false},
		{"no assistant turn yet", 0, true},
		{"stale zero with ok flag", -1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			suffix, emit := parentContextFields(tc.tokens, tc.ok)
			if emit {
				t.Fatalf("expected no parent_context_tokens for %s", tc.name)
			}
			if suffix != "" {
				t.Fatalf("expected empty human suffix, got %q", suffix)
			}
		})
	}
}
