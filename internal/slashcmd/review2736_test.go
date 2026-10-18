package slashcmd

import (
	"strings"
	"testing"
)

// Review 2736 item 1: aliases and full ids map to picker entries by family,
// version and 1M variant on token boundaries; an alias picks the first
// entry of its family as the picker lists them.
func TestRun_ModelAliasesAndIdsPickTheirEntry(t *testing.T) {
	for _, tc := range []struct{ arg, want string }{
		{"haiku", "Haiku 5.5"},
		{"opus", "Opus 5.5"},
		{"sonnet", "Sonnet 5.5"},
		{"default", "Default (recommended)"},
		{"claude-haiku-4-5-20251001", "Haiku 4.5"},
		{"claude-opus-5-5[1m]", "Opus 5.5 (1M context)"},
		{"opus[1m]", "Opus 4.6 (1M context)"},
		{"claude-opus-4-6", "Opus 4.6"},
		{"Haiku 5.5", "Haiku 5.5"},
	} {
		for _, cached := range []bool{false, true} {
			f := newFakeClaude()
			f.cached = cached
			cmd, err := Parse("/model " + tc.arg)
			if err != nil {
				t.Fatal(err)
			}
			res := Run(f, cmd, fastOpts())
			if !res.OK || f.model != tc.want || len(f.savedDefault) != 0 {
				t.Fatalf("/model %s (cached=%v): want %q session-only, got model=%q saved=%v res=%+v", tc.arg, cached, tc.want, f.model, f.savedDefault, res)
			}
		}
	}
}

// No entry matches: unknown_model, the picker cancelled, and the error lists
// the entries the picker showed. Nothing is typed blind.
func TestRun_UnknownModelListsThePickerEntries(t *testing.T) {
	f := newFakeClaude()
	cmd, _ := Parse("/model claude-nope-9")
	res := Run(f, cmd, fastOpts())
	if res.OK || res.Code != CodeUnknownModel || f.picker {
		t.Fatalf("want unknown_model with the picker cancelled: %+v", res)
	}
	for _, label := range []string{"Default (recommended)", "Opus 5.5", "Haiku 4.5", "Sonnet 4.6 (1M context)"} {
		if !strings.Contains(res.Error, label) {
			t.Fatalf("error does not list %q: %s", label, res.Error)
		}
	}
	for _, k := range f.keys {
		if strings.HasPrefix(k, "text:") && k != "text:/model " {
			t.Fatalf("typed %s besides the bare /model", k)
		}
	}
}

// Low: a numbered list in the transcript above the picker is not read as
// picker rows.
func TestModelPickerRows_OnlyThePickerRegion(t *testing.T) {
	content := strings.Join([]string{
		"⏺ Plan:", "  ❯ 1. Opus 9.9 rewrite", "    2. Haiku 9.9 port",
		"  Select model", "  ❯ 1.  Default (recommended)   Description", "    2.  Opus 5.5   Description",
	}, "\n")
	rows := modelPickerRows(content)
	if len(rows) != 2 || rows[0].label != "Default (recommended)" || !rows[0].selected || rows[1].label != "Opus 5.5" {
		t.Fatalf("rows = %+v, want only the two picker rows", rows)
	}
}

// Review 2736 item 3: the CLI reads the saved default right before the
// Enter that submits /model (after --wait-idle), never earlier.
func TestRun_BeforeSubmitRunsRightBeforeEnter(t *testing.T) {
	f := newFakeClaude()
	calls, keysAtCall := 0, -1
	opts := fastOpts()
	opts.BeforeSubmit = func() { calls++; keysAtCall = len(f.keys) }
	cmd, _ := Parse("/model haiku")
	res := Run(f, cmd, opts)
	if !res.OK || calls != 1 {
		t.Fatalf("BeforeSubmit calls = %d, res %+v", calls, res)
	}
	if keysAtCall < 0 || keysAtCall >= len(f.keys) || f.keys[keysAtCall] != "key:Enter" {
		t.Fatalf("BeforeSubmit ran at key %d of %v, want right before the submitting Enter", keysAtCall, f.keys)
	}
	// Refused before submitting: never called.
	f2 := newFakeClaude()
	f2.composer = "draft"
	calls = 0
	opts.BeforeSubmit = func() { calls++ }
	if res := Run(f2, cmd, opts); res.OK || calls != 0 {
		t.Fatalf("a refused run must not read the default: calls=%d res=%+v", calls, res)
	}
}
