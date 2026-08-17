package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Continuation prompt sources, reported on ContinuationPrompt.Source.
const (
	// ContinuationSourceAgent: the curated PROMPT.md the outgoing agent wrote
	// during its wrap-up.
	ContinuationSourceAgent = "agent"
	// ContinuationSourceTranscript: mechanically rebuilt from the on-disk
	// transcript because no curated prompt was available. A degraded handoff —
	// callers surface it loudly.
	ContinuationSourceTranscript = "transcript"
)

// ContinuationPrompt is the resolved prompt used to seed a continuation
// session, plus provenance so callers can tell a curated handoff from a
// mechanical one.
type ContinuationPrompt struct {
	Text   string
	Source string
	// Info carries transcript statistics; zero when Source is agent.
	Info HandoffInfo
}

// HandoffDir returns the per-session handoff directory,
// <project-root>/.agent-deck/handoff/<id>.
//
// A handoff prompt describes work in one repository, so it lives in that
// repository. It used to live in the machine-global data dir keyed by session
// id alone, which left every project's wrap-ups piled together in
// ~/.agent-deck/handoff with nothing tying a directory back to the project it
// came from.
//
// Empty when the session has no ID or no resolvable project path — callers
// treat that as "no curated prompt" and fall through to the transcript rebuild.
func HandoffDir(inst *Instance) string {
	if inst == nil || inst.ID == "" {
		return ""
	}
	dir, err := ProjectDataPath(instanceProjectDir(inst), "handoff", inst.ID)
	if err != nil {
		return ""
	}
	return dir
}

// EnsureHandoffDir creates HandoffDir and keeps .agent-deck/ locally ignored.
func EnsureHandoffDir(inst *Instance) (string, error) {
	if inst == nil || inst.ID == "" {
		return "", fmt.Errorf("handoff dir: session has no id")
	}
	return EnsureProjectDataPath(instanceProjectDir(inst), "handoff", inst.ID)
}

// HandoffPromptPath returns where the outgoing agent writes its curated
// continuation prompt: <project-root>/.agent-deck/handoff/<id>/PROMPT.md.
//
// It lives here, not in the UI, because the TUI writes this file and the
// `session handoff` CLI reads it — if the two ever disagreed on the location,
// the CLI would silently ignore every wrap-up the agent produced.
func HandoffPromptPath(inst *Instance) string {
	dir := HandoffDir(inst)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "PROMPT.md")
}

// HandoffChainAllows reports whether a session at sourceGeneration may produce
// one more continuation without exceeding the configured chain cap.
//
// The successor's generation is sourceGeneration+1, so with the default cap of
// 3 a human-started session (generation 0) yields successors at generations
// 1..3 and the last of those may not fork again. This is the only brake on a
// session that loops into its own ceiling and forks a successor that does the
// same.
func HandoffChainAllows(sourceGeneration int, cfg ContextBudgetSettings) bool {
	if cfg.MaxHandoffChain <= 0 {
		return false
	}
	return sourceGeneration+1 <= cfg.MaxHandoffChain
}

// ResolveContinuationPrompt returns the prompt to seed a continuation session
// with. It is the single entry point shared by the autonomous budget handoff
// and the `session handoff` CLI command.
//
// The curated PROMPT.md wins when present: the outgoing agent summarized its
// own state, which beats a raw transcript tail. When it is missing or empty —
// a wedged agent, or a wrap-up that never completed — the prompt is rebuilt
// mechanically from the transcript, which needs nothing from the agent.
//
// An error means neither source was usable, and is the sole remaining reason
// to fail a handoff outright.
func ResolveContinuationPrompt(inst *Instance, targetTool, promptPath string, maxChars int) (ContinuationPrompt, error) {
	if inst == nil {
		return ContinuationPrompt{}, fmt.Errorf("session is nil")
	}

	if promptPath != "" {
		// Read errors are not fatal here: an unreadable curated prompt is
		// exactly the case the transcript fallback exists for.
		if data, err := os.ReadFile(promptPath); err == nil && strings.TrimSpace(string(data)) != "" {
			return ContinuationPrompt{
				Text:   string(data),
				Source: ContinuationSourceAgent,
			}, nil
		}
	}

	text, info, err := BuildContinuationHandoffPrompt(inst, targetTool, maxChars)
	if err != nil {
		return ContinuationPrompt{}, fmt.Errorf("no curated handoff prompt and no usable transcript: %w", err)
	}
	return ContinuationPrompt{
		Text:   text,
		Source: ContinuationSourceTranscript,
		Info:   info,
	}, nil
}
