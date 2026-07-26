package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handoffError is a CLI-shaped failure: it carries the error code and the
// JSON-mode flag so the os.Exit wrapper renders it through CLIOutput exactly as
// the command did before the testable seam was extracted.
type handoffError struct {
	msg      string
	code     string
	jsonMode bool
}

func (e *handoffError) Error() string { return e.msg }

// handleSessionHandoff builds a cross-tool handoff prompt from a session's
// conversation history. Read-only: it never mutates the source session; the
// caller (or a future `session switch`) feeds the prompt to a new session.
func handleSessionHandoff(profile string, args []string) {
	if err := runSessionHandoff(os.Stdout, os.Stderr, profile, args); err != nil {
		var he *handoffError
		if errors.As(err, &he) {
			NewCLIOutput(he.jsonMode, false).Error(he.msg, he.code)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(1)
	}
}

// runSessionHandoff is the testable seam — handleSessionHandoff wires it to
// os.Stdout/os.Stderr and turns a returned error into the exit path; tests pass
// buffers and assert on the returned *handoffError.
func runSessionHandoff(stdout, stderr io.Writer, profile string, args []string) error {
	fs := flag.NewFlagSet("session handoff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	maxChars := fs.Int("max-chars", session.DefaultHandoffMaxChars, "Maximum transcript characters to include (tail-truncated)")
	outPath := fs.String("out", "", "Write the prompt to a file instead of stdout")
	jsonOutput := fs.Bool("json", false, "Output prompt + info as JSON")

	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: agent-deck session handoff <id|title> [options]")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Build a handoff prompt carrying the session's Claude conversation into")
		fmt.Fprintln(stderr, "another runtime (e.g. a fresh Codex session). Read-only: the source")
		fmt.Fprintln(stderr, "session is not modified. Pair with `add` + `session send` to complete")
		fmt.Fprintln(stderr, "the handoff, or use higher-level tooling that wraps this command.")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		// -h/--help: fs.Parse already printed the usage block; exit cleanly.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return &handoffError{msg: err.Error(), code: ErrCodeInvalidOperation}
	}

	identifier := fs.Arg(0)

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		return &handoffError{msg: err.Error(), code: ErrCodeNotFound, jsonMode: *jsonOutput}
	}

	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		return &handoffError{msg: errMsg, code: errCode, jsonMode: *jsonOutput}
	}

	prompt, info, err := session.BuildClaudeToCodexHandoffPrompt(inst, *maxChars)
	if err != nil {
		return &handoffError{
			msg:      fmt.Sprintf("build handoff prompt: %v", err),
			code:     ErrCodeInvalidOperation,
			jsonMode: *jsonOutput,
		}
	}

	if *jsonOutput {
		payload := struct {
			Prompt string              `json:"prompt"`
			Info   session.HandoffInfo `json:"info"`
		}{Prompt: prompt, Info: info}
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err != nil {
			return &handoffError{msg: err.Error(), code: ErrCodeInvalidOperation, jsonMode: true}
		}
		return nil
	}

	if *outPath != "" {
		if samePath(*outPath, info.TranscriptPath) {
			return &handoffError{
				msg:  "--out refuses to overwrite the source transcript",
				code: ErrCodeInvalidOperation,
			}
		}
		if err := os.WriteFile(*outPath, []byte(prompt), 0o600); err != nil {
			return &handoffError{
				msg:  fmt.Sprintf("write %s: %v", *outPath, err),
				code: ErrCodeInvalidOperation,
			}
		}
	} else {
		fmt.Fprintln(stdout, prompt)
	}
	fmt.Fprintf(stderr, "handoff: %d/%d messages included (truncated=%v, max %d chars) from %s\n",
		info.IncludedCount, info.MessageCount, info.Truncated, info.MaxChars, info.TranscriptPath)
	return nil
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
