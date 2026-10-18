package tmux

import "testing"

// A5 (1.16.29): finished Claude sessions showed a false green, running with
// substate background-work, hours after the turn ended. The pane tail in both
// owner reports is below: a completed turn, an unsent composer (a dim ghost
// prompt suggestion in one session, a typed draft in the other) and the live
// footer counter of a run_in_background shell Claude Code never reaps (a dev
// server, a tail -f). The pane alone cannot tell such a leftover shell from
// finite work, so a shell-only footer counter does not hold the session
// running here; the session layer keeps it running only while the transcript
// shows a fresh background Bash launch (internal/session/background_work.go).
const (
	a5FrameGhostSuggestion = "⏺ Done. The dev server is still up on :5173 if you want to look.\n" +
		"\n" +
		"✻ Crunched for 45s · done 10:20 PM · 1 shell still running\n" +
		"\n" +
		"────────────────────────────────────────────────────────────\n" +
		"❯ \x1b[2mrun the e2e suite against the dev server\x1b[22m\n" +
		"────────────────────────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on · 1 shell · ← for agents\n"

	a5FrameTypedDraft = "⏺ Done. The dev server is still up on :5173 if you want to look.\n" +
		"\n" +
		"✻ Crunched for 45s · done 10:20 PM · 1 shell still running\n" +
		"\n" +
		"────────────────────────────────────────────────────────────\n" +
		"❯ also check the mobile layout before we\n" +
		"────────────────────────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on · 1 shell · ← for agents\n"
)

var a5Frames = map[string]string{
	"ghost suggestion": a5FrameGhostSuggestion,
	"typed draft":      a5FrameTypedDraft,
}

func TestA5_LeftoverShellCounterAtPromptIsWaiting(t *testing.T) {
	for name, frame := range a5Frames {
		t.Run(name, func(t *testing.T) {
			if got := ClassifyPaneFrame("claude", frame); got != FrameWaiting {
				t.Errorf("frame verdict = %s, want %s: a shell-only footer counter is not work the pane can prove", got, FrameWaiting)
			}
			if sub, detail, _ := frameSubstate(frame); sub != SubstateIdleAtEmptyPrompt || detail != "" {
				t.Errorf("substate = %q (detail %q), want %q", sub, detail, SubstateIdleAtEmptyPrompt)
			}
			if claudeBackgroundWorkPending(StripANSI(frame)) {
				t.Error("a shell-only footer counter must not keep the turn open on its own")
			}
			// The counter is still recorded for the session layer, which can
			// match it against a fresh background Bash launch in the transcript.
			if w := ParseClaudeBackgroundWork(StripANSI(frame)); w.Kind != BackgroundKindBash || w.Task != "1 shell" {
				t.Errorf("recorded pane work = %+v, want bash \"1 shell\"", w)
			}
		})
	}
}
