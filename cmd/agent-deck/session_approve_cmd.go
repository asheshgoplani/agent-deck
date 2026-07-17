package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleSessionApprove resolves one visibly active Codex approval overlay with
// a single keypress. It is intentionally separate from `session send`: an
// approval is a TUI decision event, not composer text followed by Enter.
func handleSessionApprove(profile string, args []string) {
	fs := flag.NewFlagSet("session approve", flag.ExitOnError)
	fs.SetOutput(os.Stdout)
	choiceFlag := fs.String("choice", "", "Approval choice: once, always, session, or displayed option number")
	timeout := fs.Duration("timeout", 5*time.Second, "Max time to verify that the original approval prompt cleared")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("q", false, "Quiet mode")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session approve <id|title> [choice] [options]")
		fmt.Println()
		fmt.Println("Resolve one currently visible Codex approval prompt with exactly one")
		fmt.Println("keypress. Fails closed when no stable approval menu is visible.")
		fmt.Println()
		fmt.Println("Choices:")
		fmt.Println("  once       Select \"Yes, proceed\" (default)")
		fmt.Println("  always     Select the persistent/prefix approval, when offered")
		fmt.Println("  session    Select the session-scoped approval, when offered")
		fmt.Println("  1-9        Select that displayed option directly")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session approve worker")
		fmt.Println("  agent-deck session approve worker always")
		fmt.Println("  agent-deck session approve worker 2 --json")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	remaining := fs.Args()
	out := NewCLIOutput(*jsonOutput, *quiet)

	if len(remaining) < 1 {
		fs.Usage()
		out.Error("session is required", ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if len(remaining) > 2 {
		fs.Usage()
		out.Error("at most one approval choice may be specified", ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if *timeout <= 0 {
		out.Error("--timeout must be greater than zero", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	sessionRef := remaining[0]
	choice := *choiceFlag
	if len(remaining) == 2 {
		if choice != "" {
			out.Error("approval choice must be provided either positionally or with --choice, not both", ErrCodeInvalidOperation)
			os.Exit(1)
		}
		choice = remaining[1]
	}
	if choice == "" {
		choice = "once"
	}

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(sessionRef, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return
	}
	if !session.IsCodexCompatible(inst.Tool) {
		out.Error(
			fmt.Sprintf("session approve currently supports Codex sessions; '%s' uses %s", inst.Title, inst.Tool),
			ErrCodeInvalidOperation,
		)
		os.Exit(1)
	}
	if !inst.Exists() {
		out.Error(fmt.Sprintf("session '%s' is not running", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		out.Error("could not determine tmux session", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	result, approveErr := send.ApproveCodexPrompt(tmuxSess, choice, send.CodexApprovalOptions{
		VerifyTimeout: *timeout,
	})
	data := map[string]interface{}{
		"success":          approveErr == nil,
		"session_id":       inst.ID,
		"session_title":    inst.Title,
		"choice":           result.Choice,
		"option_number":    result.OptionNumber,
		"option_label":     result.OptionLabel,
		"key_sent":         result.KeySent,
		"verified":         result.Verified,
		"next_prompt_seen": result.NextPromptSeen,
	}
	if approveErr != nil {
		code := ErrCodeInvalidOperation
		if result.KeySent {
			code = ErrCodeDeliveryFailed
		}
		out.ErrorWithData(fmt.Sprintf("failed to approve Codex prompt: %v", approveErr), code, data)
		os.Exit(1)
	}

	out.Success(
		fmt.Sprintf("Approved option %d in '%s'", result.OptionNumber, inst.Title),
		data,
	)
}
