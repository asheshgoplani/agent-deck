package session

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"testing"
)

// withStubbedProbe swaps the package-level lookPathFn/statFn indirections for the
// duration of fn, so a test can simulate exactly which commands resolve on the
// host without touching the real environment. installed lists the bare command
// names that LookPath should succeed for; everything else fails.
func withStubbedProbe(t *testing.T, installed []string, fn func()) {
	t.Helper()
	set := make(map[string]bool, len(installed))
	for _, name := range installed {
		set[name] = true
	}

	origLookPath, origStat := lookPathFn, statFn
	t.Cleanup(func() { lookPathFn, statFn = origLookPath, origStat })

	lookPathFn = func(file string) (string, error) {
		if set[file] {
			return "/usr/bin/" + file, nil
		}
		return "", exec.ErrNotFound
	}
	statFn = func(name string) (os.FileInfo, error) {
		if set[name] {
			return nil, nil
		}
		return nil, &fs.PathError{Op: "stat", Path: name, Err: errors.New("no such file or directory")}
	}
	fn()
}

func commands(defs []ToolDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Command
	}
	return out
}

// TestInstalled_FilterOffNoProbe pins the core guardrail: with the flag off the
// probe never runs and All() is unchanged. We make EVERY LookPath/Stat fail; if
// the probe leaked into the default path, All() would drop entries.
func TestInstalled_FilterOffNoProbe(t *testing.T) {
	probeCalls := 0
	origLookPath, origStat := lookPathFn, statFn
	t.Cleanup(func() { lookPathFn, statFn = origLookPath, origStat })
	lookPathFn = func(string) (string, error) { probeCalls++; return "", exec.ErrNotFound }
	statFn = func(string) (os.FileInfo, error) { probeCalls++; return nil, fs.ErrNotExist }

	r := Init(nil) // filter off
	if got := len(r.All()); got != len(canonicalBuiltins) {
		t.Fatalf("All() with filter off = %d entries, want %d", got, len(canonicalBuiltins))
	}
	if got := commands(r.Visible()); !reflect.DeepEqual(got, canonicalBuiltins) {
		t.Errorf("Visible() with filter off = %v, want full %v", got, canonicalBuiltins)
	}
	if r.FilterActive() {
		t.Error("FilterActive() = true with filter off")
	}
	if probeCalls != 0 {
		t.Errorf("probe ran %d times with filter off, want 0 (probe must be gated)", probeCalls)
	}
}

// TestInstalled_FilterDropsMissing covers the happy path: with the flag on, tools
// whose command resolves are kept and missing ones are dropped — except shell.
func TestInstalled_FilterDropsMissing(t *testing.T) {
	withStubbedProbe(t, []string{"claude", "codex"}, func() {
		r := InitFiltered(nil, true)

		// All() is the UNFILTERED data view — still the full 11.
		if got := len(r.All()); got != len(canonicalBuiltins) {
			t.Errorf("All() = %d, want %d (All must stay unfiltered)", got, len(canonicalBuiltins))
		}

		// Visible() drops everything that didn't resolve, keeps shell + resolved.
		want := []string{"claude", "codex", "shell"}
		if got := commands(r.Visible()); !reflect.DeepEqual(got, want) {
			t.Errorf("Visible() = %v, want %v", got, want)
		}
		if !r.IsVisible("claude") {
			t.Error("IsVisible(claude) = false, want true (resolved)")
		}
		if r.IsVisible("gemini") {
			t.Error("IsVisible(gemini) = true, want false (not resolved)")
		}
		if r.FilterFallback() {
			t.Error("FilterFallback() = true, want false (claude/codex resolved)")
		}
	})
}

// TestInstalled_ShellAlwaysShown is the shell invariant: even when shell's own
// name would not resolve via LookPath, it is hardcoded visible.
func TestInstalled_ShellAlwaysShown(t *testing.T) {
	withStubbedProbe(t, []string{"claude"}, func() { // note: "shell" NOT in the set
		r := InitFiltered(nil, true)
		if !r.IsVisible("shell") {
			t.Error("IsVisible(shell) = false, want true (shell is always shown)")
		}
		if !r.IsVisible("") {
			t.Error("IsVisible(\"\") = false, want true (empty command is shell)")
		}
		vis := commands(r.Visible())
		if vis[len(vis)-1] != "shell" {
			t.Errorf("Visible() = %v, want shell present", vis)
		}
	})
}

// TestInstalled_CustomWrapperTreatedInstalled covers the wrapper heuristic: a
// custom command containing whitespace (and not absolute) is treated as installed
// without probing, so intentional wrapper setups are never hidden.
func TestInstalled_CustomWrapperTreatedInstalled(t *testing.T) {
	withStubbedProbe(t, []string{"claude"}, func() { // wrapper's words are NOT on PATH
		r := InitFiltered(map[string]ToolDef{
			"wrapped": {Command: "my-wrapper claude"},
			"inline":  {Command: `bash -c "exec claude"`},
		}, true)
		if !r.IsVisible("wrapped") {
			t.Error("IsVisible(wrapped) = false, want true (whitespace wrapper -> installed)")
		}
		if !r.IsVisible("inline") {
			t.Error("IsVisible(inline) = false, want true (inline shell -> installed)")
		}
	})
}

// TestInstalled_CompatibleWithOwnCommand pins the compatible_with rule: the custom
// entry's OWN command is probed, NOT the parent built-in's. A compatible_with =
// "claude" tool whose wrapper binary is missing is dropped even though claude
// itself resolves.
func TestInstalled_CompatibleWithOwnCommand(t *testing.T) {
	withStubbedProbe(t, []string{"claude"}, func() {
		r := InitFiltered(map[string]ToolDef{
			"myclaude": {Command: "my-missing-claude", CompatibleWith: "claude"},
		}, true)
		if r.IsVisible("myclaude") {
			t.Error("IsVisible(myclaude) = true, want false (own command missing; must NOT inherit claude)")
		}
		// Sanity: the built-in claude it is compatible with DID resolve.
		if !r.IsVisible("claude") {
			t.Error("IsVisible(claude) = false, want true")
		}
	})
}

// TestInstalled_EmptyFallback covers the empty-fallback safety net: when nothing
// but shell resolves, Visible()/All() return the full list (never empty) and the
// fallback signal the dialogs read is set.
func TestInstalled_EmptyFallback(t *testing.T) {
	withStubbedProbe(t, nil, func() { // nothing resolves
		r := InitFiltered(nil, true)
		if !r.FilterFallback() {
			t.Fatal("FilterFallback() = false, want true (nothing but shell resolved)")
		}
		// Fallback shows everything, not an empty selection.
		if got := commands(r.Visible()); !reflect.DeepEqual(got, canonicalBuiltins) {
			t.Errorf("Visible() in fallback = %v, want full %v", got, canonicalBuiltins)
		}
		if !r.IsVisible("gemini") {
			t.Error("IsVisible(gemini) = false in fallback, want true (show all)")
		}
	})
}

// TestInstalled_FilterVisibleNames covers the TUI call-site helper: hidden names
// are dropped, "" (shell) is always kept, and with the filter off the input is
// returned byte-identically.
func TestInstalled_FilterVisibleNames(t *testing.T) {
	presets := []string{"", "claude", "gemini", "codex"}

	// Filter off: identity.
	if got := Init(nil).FilterVisibleNames(presets); !reflect.DeepEqual(got, presets) {
		t.Errorf("FilterVisibleNames (off) = %v, want identity %v", got, presets)
	}

	withStubbedProbe(t, []string{"claude"}, func() {
		r := InitFiltered(nil, true)
		want := []string{"", "claude"} // "" kept (shell), gemini/codex dropped
		if got := r.FilterVisibleNames(presets); !reflect.DeepEqual(got, want) {
			t.Errorf("FilterVisibleNames (on) = %v, want %v", got, want)
		}
	})
}

// TestInstalled_DispatchIgnoresFilter is deliverable #5: the resolution path the
// CLI dispatch uses (Match) must still resolve a tool the filter would hide. We
// build a filtered registry where gemini did NOT resolve, then confirm Match
// still maps a gemini command to "gemini".
func TestInstalled_DispatchIgnoresFilter(t *testing.T) {
	withStubbedProbe(t, nil, func() { // gemini (and everything) hidden
		r := InitFiltered(nil, true)
		if r.IsVisible("gemini") && !r.FilterFallback() {
			t.Fatal("precondition: gemini should be hidden by the filter")
		}
		// Dispatch resolution is display-independent: Match still returns gemini.
		if got := r.Match("gemini --yolo"); got != "gemini" {
			t.Errorf("Match(\"gemini --yolo\") = %q, want %q (filter must not gate dispatch)", got, "gemini")
		}
	})
}

// TestInstalled_AbsolutePathCustom covers the os.Stat arm for absolute-path custom
// commands: present on disk -> visible, absent -> hidden.
func TestInstalled_AbsolutePathCustom(t *testing.T) {
	withStubbedProbe(t, []string{"/opt/bin/real-tool"}, func() {
		r := InitFiltered(map[string]ToolDef{
			"real":  {Command: "/opt/bin/real-tool"},
			"ghost": {Command: "/opt/bin/missing-tool"},
		}, true)
		if !r.IsVisible("real") {
			t.Error("IsVisible(real) = false, want true (absolute path exists)")
		}
		if r.IsVisible("ghost") {
			t.Error("IsVisible(ghost) = true, want false (absolute path missing)")
		}
	})
}

// TestInstalled_CustomNamesUnaffectedByFilter confirms CustomNames() (the data
// view) is unfiltered; only the visibility surface filters.
func TestInstalled_CustomNamesUnaffectedByFilter(t *testing.T) {
	withStubbedProbe(t, nil, func() {
		r := InitFiltered(map[string]ToolDef{
			"a": {Command: "missing-a"},
			"b": {Command: "missing-b"},
		}, true)
		got := r.CustomNames()
		sort.Strings(got)
		if !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Errorf("CustomNames() = %v, want [a b] (data view unfiltered)", got)
		}
	})
}
