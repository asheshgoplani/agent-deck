package slashcmd

import (
	"strings"
	"testing"
	"time"
)

func fastOpts() Options {
	return Options{Timeout: 2 * time.Second, TypeWait: 20 * time.Millisecond, Poll: time.Millisecond, Settle: time.Millisecond}
}

func TestParse(t *testing.T) {
	cases := []struct {
		in         string
		name, args string
		kind       Kind
	}{
		{"/model claude-haiku-5-5", "/model", "claude-haiku-5-5", KindModel},
		{"  /compact ", "/compact", "", KindCompact},
		{"/clear", "/clear", "", KindClear},
		{"/exit", "/exit", "", KindExit},
		{"/quit", "/quit", "", KindExit},
		{"/voice-mode:on", "/voice-mode:on", "", KindGeneric},
		{"/loop 5m /catchup", "/loop", "5m /catchup", KindGeneric},
	}
	for _, c := range cases {
		cmd, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		if cmd.Name != c.name || cmd.Args != c.args || cmd.Kind != c.kind {
			t.Fatalf("Parse(%q) = %+v", c.in, cmd)
		}
	}
	for _, bad := range []string{"", "model", "/", "/ model", "/model\nrm", "/model\x1b[A", "/mo del\tx"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("Parse(%q) must fail", bad)
		}
	}
}

func TestRun_ModelSwitchIsVerifiedFromTheEcho(t *testing.T) {
	f := newFakeClaude()
	cmd, _ := Parse("/model claude-haiku-5-5")
	res := Run(f, cmd, fastOpts())
	if !res.OK {
		t.Fatalf("model switch failed: %+v", res)
	}
	if res.Before != "Fable 5.1" || res.After != "Haiku 5.5" {
		t.Fatalf("before/after = %q/%q", res.Before, res.After)
	}
	if len(res.Evidence) == 0 || !strings.Contains(res.Evidence[0], "Set model to Haiku 5.5") {
		t.Fatalf("evidence missing the echo: %v", res.Evidence)
	}
	if got := strings.Join(f.executed, "|"); got != "/model claude-haiku-5-5" {
		t.Fatalf("executed %q", got)
	}
}

func TestRun_BareCommandGetsTrailingSpaceSoThePickerCloses(t *testing.T) {
	f := newFakeClaude()
	f.transcript = []string{"❯ hello", "⏺ hi"}
	cmd, _ := Parse("/clear")
	res := Run(f, cmd, fastOpts())
	if !res.OK {
		t.Fatalf("clear failed: %+v", res)
	}
	if f.keys[0] != "text:/clear " {
		t.Fatalf("first key = %q, want the command plus one space", f.keys[0])
	}
}

func TestRun_CompactIsVerifiedFromTheNotice(t *testing.T) {
	f := newFakeClaude()
	f.transcript = []string{"❯ list primes", "⏺ 2 3 5"}
	cmd, _ := Parse("/compact")
	res := Run(f, cmd, fastOpts())
	if !res.OK || res.After != "compacted" {
		t.Fatalf("compact: %+v", res)
	}
}

func TestRun_CompactTimesOutWithoutTheNotice(t *testing.T) {
	f := newFakeClaude()
	f.afterExec = func(f *fakeClaude, cmd string) {
		// Compaction still running: no notice, spinner up.
		f.transcript = f.transcript[:len(f.transcript)-1]
		f.busy = true
	}
	cmd, _ := Parse("/compact")
	opts := fastOpts()
	opts.Timeout = 50 * time.Millisecond
	res := Run(f, cmd, opts)
	if res.OK || res.Code != CodeVerifyTimeout {
		t.Fatalf("want verify_timeout, got %+v", res)
	}
}

func TestRun_RefusesWhenTheComposerHoldsUnsentText(t *testing.T) {
	f := newFakeClaude()
	f.composer = "my unsent draft"
	f.escaped = true
	cmd, _ := Parse("/compact")
	res := Run(f, cmd, fastOpts())
	if res.OK || res.Code != "composer_draft" {
		t.Fatalf("want composer_draft, got %+v", res)
	}
	if len(f.keys) != 0 {
		t.Fatalf("nothing may be typed over a draft, sent %v", f.keys)
	}
	if f.composer != "my unsent draft" {
		t.Fatalf("draft changed to %q", f.composer)
	}
}

func TestRun_GhostSuggestionIsReplacedNotAccepted(t *testing.T) {
	f := newFakeClaude()
	f.ghost = "add a subtract function"
	cmd, _ := Parse("/clear")
	res := Run(f, cmd, fastOpts())
	if !res.OK {
		t.Fatalf("clear over a ghost failed: %+v", res)
	}
	for _, k := range f.keys {
		if k == "key:Tab" || k == "key:Right" {
			t.Fatalf("ghost accepted with %s", k)
		}
	}
	if got := strings.Join(f.executed, "|"); got != "/clear" {
		t.Fatalf("executed %q", got)
	}
}

func TestRun_PickerOnADifferentEntryIsNeverSelected(t *testing.T) {
	f := newFakeClaude()
	cmd, _ := Parse("/mod")
	res := Run(f, cmd, fastOpts())
	if res.OK || res.Code != CodeUnknownCommand {
		t.Fatalf("want unknown_command, got %+v", res)
	}
	if len(f.executed) != 0 {
		t.Fatalf("Claude ran %v; the highlighted picker entry must never be submitted", f.executed)
	}
	for _, k := range f.keys {
		if k == "key:Enter" {
			t.Fatal("Enter pressed while the picker pointed at another entry")
		}
	}
	if f.composer != "" {
		t.Fatalf("our typed text must be removed, composer = %q", f.composer)
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "/model") {
		t.Fatalf("evidence should name the entry the picker would have run: %v", res.Evidence)
	}
}

func TestRun_UnknownCommandErrorIsReported(t *testing.T) {
	f := newFakeClaude()
	// The picker knows /nosuch (so it closes on the space), but Claude
	// rejects it on Enter.
	f.known = append(f.known, "/nosuch")
	cmd, _ := Parse("/nosuch")
	res := Run(&rejecting{f}, cmd, fastOpts())
	if res.OK || res.Code != CodeCommandError {
		t.Fatalf("want command_error, got %+v", res)
	}
}

// rejecting makes Claude reject the command on Enter with "Unknown command".
type rejecting struct{ *fakeClaude }

func (r *rejecting) SendNamedKey(key string) error {
	if key != "Enter" {
		return r.fakeClaude.SendNamedKey(key)
	}
	r.known = r.known[:len(r.known)-1]
	r.fakeClaude.keys = append(r.fakeClaude.keys, "key:Enter")
	name := strings.TrimSpace(r.composer)
	r.run(name, "")
	return nil
}

func TestRun_BusyTargetIsRefusedWithoutTyping(t *testing.T) {
	f := newFakeClaude()
	f.busy = true
	cmd, _ := Parse("/compact")
	res := Run(f, cmd, fastOpts())
	if res.OK || res.Code != "agent_busy" || len(f.keys) != 0 {
		t.Fatalf("want agent_busy with no keys, got %+v keys=%v", res, f.keys)
	}
}

func TestRun_WaitIdleWaitsForTheTurnToEnd(t *testing.T) {
	f := newFakeClaude()
	f.busy = true
	polls := 0
	opts := fastOpts()
	opts.WaitIdle = time.Second
	opts.Sleep = func(time.Duration) {
		polls++
		if polls == 3 {
			f.busy = false
		}
	}
	cmd, _ := Parse("/clear")
	res := Run(f, cmd, opts)
	if !res.OK {
		t.Fatalf("wait-idle run failed: %+v", res)
	}
}

func TestRun_ModelWithoutArgumentOpensDialogWhichIsCancelled(t *testing.T) {
	f := newFakeClaude()
	cmd, _ := Parse("/model")
	res := Run(f, cmd, fastOpts())
	if res.OK || res.Code != CodeInteractive {
		t.Fatalf("want interactive_command, got %+v", res)
	}
	if f.dialog {
		t.Fatal("the dialog our command opened must be cancelled")
	}
	if f.model != "Fable 5.1" {
		t.Fatalf("model changed to %q", f.model)
	}
}

func TestRun_ExitIsVerifiedByThePaneGoingAway(t *testing.T) {
	f := newFakeClaude()
	cmd, _ := Parse("/exit")
	res := Run(f, cmd, fastOpts())
	if !res.OK || res.After != "exited" {
		t.Fatalf("exit: %+v", res)
	}
}

func TestRun_GenericSkillIsVerifiedByTheEcho(t *testing.T) {
	f := newFakeClaude()
	cmd, _ := Parse("/catchup")
	res := Run(f, cmd, fastOpts())
	if !res.OK || res.After != "submitted" {
		t.Fatalf("skill: %+v", res)
	}
}

func TestRun_DryRunTypesNothing(t *testing.T) {
	f := newFakeClaude()
	opts := fastOpts()
	opts.DryRun = true
	cmd, _ := Parse("/compact")
	res := Run(f, cmd, opts)
	if !res.OK || len(f.keys) != 0 || len(res.Plan) == 0 {
		t.Fatalf("dry run: %+v keys=%v", res, f.keys)
	}
}

func TestRun_TextChangedUnderUsIsNeverSubmittedOrCleared(t *testing.T) {
	f := newFakeClaude()
	cmd, _ := Parse("/compact")
	res := Run(&interleaving{f}, cmd, fastOpts())
	if res.OK || res.Code != CodeTypeMismatch {
		t.Fatalf("want type_mismatch, got %+v", res)
	}
	for _, k := range f.keys {
		if k == "key:Enter" || k == "key:C-u" {
			t.Fatalf("pressed %s on a composer holding someone else's text", k)
		}
	}
}

// interleaving simulates the owner typing at the same moment.
type interleaving struct{ *fakeClaude }

func (i *interleaving) SendKeys(text string) error {
	i.composer += "hey "
	return i.fakeClaude.SendKeys(text)
}

func TestRun_ModelSwitchConfirmsOnlyTheRequestedSwitch(t *testing.T) {
	f := newFakeClaude()
	f.model = "Haiku 5.5"
	f.cached = true
	cmd, _ := Parse("/model claude-fable-5-1")
	res := Run(f, cmd, fastOpts())
	if !res.OK || res.After != "Fable 5.1" {
		t.Fatalf("switch with confirmation: %+v", res)
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "Yes, switch to Fable 5.1") {
		t.Fatalf("evidence must record the confirmation: %v", res.Evidence)
	}
}

func hasKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

// Review B1: a turn that starts after typing must not be interrupted.
func TestRun_TurnStartingBeforeEnterIsNotInterrupted(t *testing.T) {
	f := newFakeClaude()
	f.beforeCapture = func(f *fakeClaude, n int) {
		if n == 3 { // preflight, confirmTyped, then the last look before Enter
			f.busy = true
		}
	}
	cmd, _ := Parse("/compact")
	res := Run(f, cmd, fastOpts())
	if res.OK || res.Code != "agent_busy" {
		t.Fatalf("want agent_busy, got %+v", res)
	}
	if hasKey(f.keys, "key:Escape") || hasKey(f.keys, "key:Enter") {
		t.Fatalf("Escape interrupts a turn and Enter would queue the command; keys %v", f.keys)
	}
	if f.composer != "" {
		t.Fatalf("our typed text must be erased with Ctrl+U, composer %q", f.composer)
	}
}

// Review S1: keys landing between the confirmation and Enter are not submitted.
func TestRun_KeysTypedRightBeforeEnterAreNotSubmitted(t *testing.T) {
	f := newFakeClaude()
	f.beforeCapture = func(f *fakeClaude, n int) {
		if n == 3 {
			f.composer += "and my note"
		}
	}
	cmd, _ := Parse("/compact")
	res := Run(f, cmd, fastOpts())
	if res.OK || res.Code != CodeTypeMismatch || hasKey(f.keys, "key:Enter") || hasKey(f.keys, "key:C-u") {
		t.Fatalf("want type_mismatch with no Enter or Ctrl+U, got %+v keys=%v", res, f.keys)
	}
}

// Review S3: a "Switch model?" dialog naming another model is cancelled.
func TestRun_SwitchDialogForAnotherModelIsCancelled(t *testing.T) {
	f := newFakeClaude()
	f.model = "Haiku 5.5"
	f.cached = true
	f.switchTo = "Opus 5.5"
	cmd, _ := Parse("/model claude-fable-5-1")
	res := Run(f, cmd, fastOpts())
	if res.OK || res.Code != CodeCommandError {
		t.Fatalf("want command_error, got %+v", res)
	}
	if f.model != "Haiku 5.5" || f.pendingModel != "" {
		t.Fatalf("model must stay and the dialog must be cancelled: model=%q pending=%q", f.model, f.pendingModel)
	}
}

// Review S4: an error rendered a frame after the echo is still caught.
func TestRun_LateUnknownCommandIsNotReportedAsSuccess(t *testing.T) {
	f := newFakeClaude()
	f.lateError = true
	cmd, _ := Parse("/catchup")
	res := Run(f, cmd, fastOpts())
	if res.OK || res.Code != CodeCommandError {
		t.Fatalf("want command_error, got %+v", res)
	}
}

// Review S5: /clear is not reported until the old transcript is gone.
func TestRun_ClearNeedsTheOldTranscriptGone(t *testing.T) {
	f := newFakeClaude()
	f.transcript = []string{"❯ list files", "  README.md main.go"} // no ⏺ line in view
	f.afterExec = func(f *fakeClaude, cmd string) {
		f.transcript = []string{"❯ list files", "  README.md main.go", "❯ /clear"} // Claude has not redrawn
	}
	cmd, _ := Parse("/clear")
	opts := fastOpts()
	opts.Timeout = 30 * time.Millisecond
	res := Run(f, cmd, opts)
	if res.OK {
		t.Fatalf("clear must not be verified while the old transcript is on screen: %+v", res)
	}
}

// Review S6: a skill's own permission prompt is left for the owner.
func TestRun_SkillPermissionPromptIsNotDenied(t *testing.T) {
	f := newFakeClaude()
	f.permissionAfter = true
	cmd, _ := Parse("/catchup")
	res := Run(f, cmd, fastOpts())
	if !res.OK || hasKey(f.keys, "key:Escape") {
		t.Fatalf("want ok without Escape, got %+v keys=%v", res, f.keys)
	}
}

// Review S7: Enter that does not submit leaves no command behind.
func TestRun_EnterNotAcceptedErasesOurCommand(t *testing.T) {
	f := newFakeClaude()
	f.ignoreEnter = true
	cmd, _ := Parse("/compact")
	opts := fastOpts()
	opts.Timeout = 30 * time.Millisecond
	res := Run(f, cmd, opts)
	if res.OK || res.Code != CodeEnterIgnored || f.composer != "" {
		t.Fatalf("want enter_not_accepted and an empty composer, got %+v composer=%q", res, f.composer)
	}
}

// Review S8: hint-styled text in front of our command is not ignored.
func TestComposerInput_HintOnlyAsTrailingSuffix(t *testing.T) {
	div := strings.Repeat("─", 60)
	pane := func(body string) string { return strings.Join([]string{div, "❯ " + body, div}, "\n") }
	if got := composerInput(pane("\x1b[38;5;153m/clear\x1b[39m  \x1b[38;5;246m[name]\x1b[39m")); got != "/clear" {
		t.Fatalf("trailing arg hint must be dropped, got %q", got)
	}
	if got := composerInput(pane("\x1b[2mmy pasted text \x1b[0m/clear")); got == "/clear" {
		t.Fatal("dim text in front of the command must not be dropped")
	}
}

// Review S9: picker rows without a description still count as an open picker.
func TestReadPicker_RowWithoutDescription(t *testing.T) {
	div := strings.Repeat("─", 60)
	content := strings.Join([]string{div, "❯ /dep", div, "  ❯ /deploy", "    /deps-audit      Audit dependencies"}, "\n")
	st := readPicker(content)
	if !st.open || st.selected != "/deploy" {
		t.Fatalf("picker = %+v", st)
	}
}
