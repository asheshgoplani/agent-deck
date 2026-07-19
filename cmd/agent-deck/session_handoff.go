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

	prompt, info, err := session.BuildClaudeToCodexHandoffPrompt(inst, *maxChars)
	if err != nil {
		out.Error(fmt.Sprintf("build handoff prompt: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if *jsonOutput {
		payload := struct {
			Prompt string              `json:"prompt"`
			Info   session.HandoffInfo `json:"info"`
		}{Prompt: prompt, Info: info}
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
	fmt.Fprintf(os.Stderr, "handoff: %d/%d messages included (truncated=%v, max %d chars) from %s\n",
		info.IncludedCount, info.MessageCount, info.Truncated, info.MaxChars, info.TranscriptPath)
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
