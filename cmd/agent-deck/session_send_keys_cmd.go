package main

import (
	"flag"
	"fmt"
	"os"
)

// handleSessionSendKeys implements `agent-deck session send-keys <id>
// [--text TEXT] [--enter] [--named-key KEY]` — the low-level keystroke
// dispatch the TUI's insert mode invokes on a REMOTE agent-deck instance
// over SSH (#1102 bug 2). The locally-running agent-deck TUI uses the
// in-process tmux.KeySender for local sessions; remote sessions land here
// via the RemoteKeySender → SSHRunner.Run → ssh → this CLI handler.
//
// Distinct from `session send`:
//   - `send` is the high-level "wait for agent, deliver a message, optionally
//     wait for response" flow used by users at the CLI.
//   - `send-keys` is the low-level "type these bytes into the pane right
//     now" primitive used by the TUI; it does no readiness checking and
//     returns immediately so the TUI's interactive latency stays low.
//
// Flags are mutually exclusive in the dispatch sense: exactly one of --text,
// --enter, or --named-key per invocation. This keeps the wire protocol
// trivial and avoids encoding ordering ("text then enter? enter then named?")
// at the CLI boundary — the TUI calls once per logical key it wants to send.
func handleSessionSendKeys(profile string, args []string) {
	fs := flag.NewFlagSet("session send-keys", flag.ExitOnError)
	fs.SetOutput(os.Stdout)
	text := fs.String("text", "", "Literal text to type (tmux send-keys -l)")
	namedKey := fs.String("named-key", "", "Tmux named key (e.g. BSpace, Up, C-c)")
	sendEnter := fs.Bool("enter", false, "Send an Enter keystroke")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("q", false, "Quiet mode")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session send-keys <id|title> [options]")
		fmt.Println()
		fmt.Println("Forward a single keystroke (or rune burst) to a running session's")
		fmt.Println("tmux pane. Used by the TUI insert mode (#1069/#1094/#1102); not")
		fmt.Println("typically invoked by users directly.")
		fmt.Println()
		fmt.Println("Exactly one of --text, --named-key, or --enter must be specified.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session send-keys my-project --text \"hello\"")
		fmt.Println("  agent-deck session send-keys my-project --named-key BSpace")
		fmt.Println("  agent-deck session send-keys my-project --enter")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	remaining := fs.Args()

	out := NewCLIOutput(*jsonOutput, *quiet)

	if len(remaining) < 1 {
		fs.Usage()
		out.Error("session id or title is required", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Validate exactly one dispatch flag.
	count := 0
	if *text != "" {
		count++
	}
	if *namedKey != "" {
		count++
	}
	if *sendEnter {
		count++
	}
	if count != 1 {
		out.Error("exactly one of --text, --named-key, or --enter required", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	sessionRef := remaining[0]

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

	if !inst.Exists() {
		out.Error(fmt.Sprintf("session '%s' is not running", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		out.Error(fmt.Sprintf("session '%s' has no tmux pane", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	switch {
	case *text != "":
		if err := tmuxSess.SendKeys(*text); err != nil {
			out.Error(fmt.Sprintf("send-keys failed: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	case *namedKey != "":
		if err := tmuxSess.SendNamedKey(*namedKey); err != nil {
			out.Error(fmt.Sprintf("send-named-key failed: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	case *sendEnter:
		if err := tmuxSess.SendEnter(); err != nil {
			out.Error(fmt.Sprintf("send-enter failed: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	out.Success("", map[string]interface{}{
		"success":       true,
		"session_id":    inst.ID,
		"session_title": inst.Title,
	})
}
