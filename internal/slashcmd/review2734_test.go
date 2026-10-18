package slashcmd

import (
	"errors"
	"strings"
	"testing"
)

// Review M1: the requested model is matched to Claude's echo by family,
// version and 1M variant on token boundaries.
func TestModelMatches(t *testing.T) {
	for _, tc := range []struct {
		arg, display string
		want         bool
	}{
		{"opus[1m]", "Opus 4.6 (1M context)", true},
		{"opus[1m]", "Opus 4.6", false},
		{"opus", "Opus 4.6 (1M context)", false},
		{"sonnet[1m]", "Sonnet 4.6 (1M context)", true},
		{"s", "Sonnet 4.6", false},
		{"opus-4", "Opus 4.6", false},
		{"opus-4-6", "Opus 4.6", true},
		{"claude-opus-4-6", "Opus 4.6", true},
		{"claude-opus-5-5[1m]", "Opus 5.5 (1M context)", true},
		{"claude-haiku-4-5-20251001", "Haiku 4.5", true},
		{"claude-haiku-5-5", "Haiku 5.5", true},
		{"claude-haiku-5-5", "Haiku 5", false},
		{"haiku", "Haiku 5.5", true},
		{"haiku", "Sonnet 4.6", false},
		{"Haiku 5.5", "Haiku 5.5", true},
		{"default", "Opus 5.5", true},
		{"", "Opus 5.5", false},
	} {
		if got := modelMatches(tc.arg, tc.display); got != tc.want {
			t.Errorf("modelMatches(%q, %q) = %v, want %v", tc.arg, tc.display, got, tc.want)
		}
	}
}

// opus[1m] used to report command_error, and with a cached conversation the
// "Yes, switch to Opus 4.6 (1M context)" dialog was cancelled with Escape.
func TestRun_OneMillionContextAliasSwitches(t *testing.T) {
	for _, cached := range []bool{false, true} {
		f := newFakeClaude()
		f.cached = cached
		cmd, _ := Parse("/model opus[1m]")
		res := Run(f, cmd, fastOpts())
		if !res.OK || res.After != "Opus 4.6 (1M context)" || f.model != "Opus 4.6 (1M context)" {
			t.Fatalf("cached=%v: /model opus[1m] = %+v (model %q)", cached, res, f.model)
		}
		if hasKey(f.keys, "key:Escape") {
			t.Fatalf("cached=%v: Escape pressed on the requested switch: %v", cached, f.keys)
		}
	}
}

// A too-loose match ("s" -> Sonnet, "opus-4" -> Opus 4.6) is reported, and a
// "Switch model?" for it is cancelled, never accepted.
func TestRun_LooseModelMatchIsNotAccepted(t *testing.T) {
	for _, arg := range []string{"s", "opus-4"} {
		for _, cached := range []bool{false, true} {
			f := newFakeClaude()
			f.cached = cached
			cmd, _ := Parse("/model " + arg)
			res := Run(f, cmd, fastOpts())
			if res.OK || res.Code != CodeCommandError {
				t.Fatalf("/model %s cached=%v: want command_error, got %+v", arg, cached, res)
			}
			if cached && f.model != "Fable 5.1" {
				t.Fatalf("/model %s: the switch to %s was accepted", arg, f.model)
			}
		}
	}
}

// L5: /login and /logout are refused before anything is typed.
func TestParse_RefusesLoginAndLogout(t *testing.T) {
	for _, s := range []string{"/login", "/logout", "/logout now"} {
		if _, err := Parse(s); !errors.Is(err, ErrRefused) {
			t.Fatalf("Parse(%q) err = %v, want ErrRefused", s, err)
		}
	}
	if _, err := Parse("/loginx"); err != nil {
		t.Fatalf("Parse(/loginx) = %v, want ok", err)
	}
}

// L3: a dialog with our echo scrolled off may be a skill's own permission
// prompt; it is reported and never answered or cancelled.
func TestRun_DialogWithoutVisibleEchoIsLeftUntouched(t *testing.T) {
	f := newFakeClaude()
	f.permissionAfter = true
	f.afterExec = func(f *fakeClaude, _ string) {
		f.transcript = []string{"⏺ Bash(make test)", "  ⎿  running"} // echo scrolled off
	}
	cmd, _ := Parse("/catchup")
	res := Run(f, cmd, fastOpts())
	if res.OK || res.Code != CodeDialogUnowned {
		t.Fatalf("want dialog_open, got %+v", res)
	}
	if hasKey(f.keys, "key:Escape") || !f.dialog {
		t.Fatalf("the dialog was touched: keys=%v dialog=%v", f.keys, f.dialog)
	}
}

// L3: a generic command whose echo scrolled off before the first poll ran:
// our text left the composer and the transcript changed.
func TestRun_GenericEchoScrolledOffStillVerifies(t *testing.T) {
	f := newFakeClaude()
	f.afterExec = func(f *fakeClaude, _ string) {
		f.transcript = []string{"⏺ " + strings.Repeat("long output ", 20)}
	}
	cmd, _ := Parse("/catchup")
	opts := fastOpts()
	res := Run(f, cmd, opts)
	if !res.OK || res.After != "submitted" {
		t.Fatalf("want submitted, got %+v", res)
	}
}

// L4: text that arrives mangled (tmux ate a trailing ';') is never
// submitted and is cleared, so no partial command stays in the composer.
func TestRun_MangledOwnTextIsClearedOnTypeMismatch(t *testing.T) {
	f := newFakeClaude()
	f.eatTrailingSemicolon = true
	cmd, _ := Parse("/status a;")
	res := Run(f, cmd, fastOpts())
	if res.OK || res.Code != CodeTypeMismatch {
		t.Fatalf("want type_mismatch, got %+v", res)
	}
	if hasKey(f.keys, "key:Enter") || !hasKey(f.keys, "key:C-u") || f.composer != "" {
		t.Fatalf("want the partial command cleared and nothing submitted: keys=%v composer=%q", f.keys, f.composer)
	}
}

// Test gap: an unknown command with the picker open while a turn starts
// must be removed with Ctrl+U only; Escape would interrupt the turn.
func TestRun_UnknownCommandDuringTurnIsRemovedWithoutEscape(t *testing.T) {
	f := newFakeClaude()
	f.beforeCapture = func(f *fakeClaude, _ int) {
		if f.composer != "" {
			f.busy = true // a turn started right after we typed
		}
	}
	cmd, _ := Parse("/stat")
	res := Run(f, cmd, fastOpts())
	if res.OK || res.Code != CodeUnknownCommand {
		t.Fatalf("want unknown_command, got %+v", res)
	}
	if hasKey(f.keys, "key:Escape") {
		t.Fatalf("Escape pressed during a running turn: %v", f.keys)
	}
	for _, l := range f.transcript {
		if strings.Contains(l, "Interrupted") {
			t.Fatalf("the turn was interrupted: %v", f.transcript)
		}
	}
	if !hasKey(f.keys, "key:C-u") || f.composer != "" {
		t.Fatalf("our text must be removed with Ctrl+U: keys=%v composer=%q", f.keys, f.composer)
	}
}
