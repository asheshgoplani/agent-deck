package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleSessionHandoff builds a cross-tool handoff prompt from a session's
// conversation history. Read-only: it never mutates the source session; the
// caller (or a future `session switch`) feeds the prompt to a new session.
func handleSessionHandoff(profile string, args []string) {
	fs := flag.NewFlagSet("session handoff", flag.ExitOnError)
	maxChars := fs.Int("max-chars", session.DefaultHandoffMaxChars, "Maximum transcript characters to include (tail-truncated)")
	outPath := fs.String("out", "", "Write the prompt to a file instead of stdout")
	jsonOutput := fs.Bool("json", false, "Output prompt + info as JSON")
	targetTool := fs.String("target-tool", "codex", "Tool the continuation will run (bare name, e.g. \"codex\"); same tool as the source produces continuation framing instead of cross-tool handoff framing")
	ignoreAgentPrompt := fs.Bool("ignore-agent-prompt", false, "Ignore any curated PROMPT.md left by a wrap-up and always rebuild from the transcript")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session handoff <id|title> [options]")
		fmt.Println()
		fmt.Println("Build a handoff prompt carrying the session's Claude conversation into")
		fmt.Println("another runtime (e.g. a fresh Codex session). Read-only: the source")
		fmt.Println("session is not modified. Pair with `add` + `session send` to complete")
		fmt.Println("the handoff, or use higher-level tooling that wraps this command.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	out := NewCLIOutput(*jsonOutput, false)

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(1)
	}

	resolved, err := resolveSessionHandoff(inst, *targetTool, *ignoreAgentPrompt, *maxChars)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	prompt, info := resolved.Text, resolved.Info

	if *jsonOutput {
		payload := struct {
			Prompt string              `json:"prompt"`
			Source string              `json:"source"`
			Info   session.HandoffInfo `json:"info"`
		}{Prompt: prompt, Source: resolved.Source, Info: info}
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err != nil {
			out.Error(err.Error(), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		return
	}

	if *outPath != "" {
		if samePath(*outPath, info.TranscriptPath) {
			out.Error("--out refuses to overwrite the source transcript", ErrCodeInvalidOperation)
			os.Exit(1)
		}
		if err := os.WriteFile(*outPath, []byte(prompt), 0o600); err != nil {
			out.Error(fmt.Sprintf("write %s: %v", *outPath, err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	} else {
		fmt.Println(prompt)
	}
	if resolved.Source == session.ContinuationSourceAgent {
		fmt.Fprintf(os.Stderr, "handoff: using the agent's curated prompt from %s (pass --ignore-agent-prompt to rebuild from the transcript)\n", session.HandoffPromptPath(inst.ID))
	} else {
		fmt.Fprintf(os.Stderr, "handoff: %d/%d messages included (truncated=%v, max %d chars) from %s\n",
			info.IncludedCount, info.MessageCount, info.Truncated, info.MaxChars, info.TranscriptPath)
	}
}

// resolveSessionHandoff validates the requested target tool and resolves the
// continuation prompt for a session, honoring --ignore-agent-prompt by
// suppressing the curated PROMPT.md lookup. It shares
// session.ResolveContinuationPrompt with the autonomous budget handoff so a
// curated PROMPT.md left by a wrap-up is honored here too instead of being
// silently ignored in favour of the raw transcript. Split out of
// handleSessionHandoff so the flag routing (target-tool validation, the
// --ignore-agent-prompt path suppression, and computing the curated path from
// inst.ID) is unit-testable without os.Exit or a loaded profile.
func resolveSessionHandoff(inst *session.Instance, targetTool string, ignoreAgentPrompt bool, maxChars int) (session.ContinuationPrompt, error) {
	if err := session.ValidateHandoffTargetTool(targetTool); err != nil {
		return session.ContinuationPrompt{}, err
	}
	var promptPath string
	if inst != nil && !ignoreAgentPrompt {
		promptPath = session.HandoffPromptPath(inst.ID)
	}
	resolved, err := session.ResolveContinuationPrompt(inst, targetTool, promptPath, maxChars)
	if err != nil {
		return session.ContinuationPrompt{}, fmt.Errorf("build handoff prompt: %v", err)
	}
	return resolved, nil
}

// samePath reports whether two paths refer to the same file, following
// symlinks when the targets exist.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil {
		return ra == rb
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(absA) == filepath.Clean(absB)
}
