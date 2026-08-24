package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleSessionApproval inspects one live Codex approval overlay without
// sending terminal input.
func handleSessionApproval(profile string, args []string) {
	fs := flag.NewFlagSet("session approval", flag.ExitOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("q", false, "Quiet mode")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session approval <id|title> [options]")
		fmt.Println()
		fmt.Println("Inspect one currently visible Codex approval prompt without sending input.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	out := NewCLIOutput(*jsonOutput, *quiet)
	if fs.NArg() != 1 {
		fs.Usage()
		out.Error("session is required", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}
	inst, message, code := ResolveSession(fs.Arg(0), instances)
	if inst == nil {
		out.Error(message, code)
		if code == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return
	}
	if !session.IsCodexCompatible(inst.Tool) {
		out.Error(
			fmt.Sprintf("session approval currently supports Codex sessions; '%s' uses %s", inst.Title, inst.Tool),
			ErrCodeInvalidOperation,
		)
		os.Exit(1)
	}
	if !inst.Exists() {
		out.Error(fmt.Sprintf("session '%s' is not running", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	target := inst.GetTmuxSession()
	if target == nil {
		out.Error("could not determine tmux session", ErrCodeInvalidOperation)
		os.Exit(1)
	}
	request, inspectErr := send.InspectCodexApprovalPrompt(target)
	if inspectErr != nil {
		out.Error(fmt.Sprintf("failed to inspect Codex prompt: %v", inspectErr), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	data := map[string]interface{}{
		"active":        request != nil,
		"session_id":    inst.ID,
		"session_title": inst.Title,
	}
	if request != nil {
		data["fingerprint"] = request.Fingerprint
		data["context"] = request.Context
		data["options"] = request.Options
	}
	out.Success("Inspected Codex approval prompt", data)
}
