package main

import (
	"bufio"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
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
	fs := flag.NewFlagSet("session send-keys", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	text := fs.String("text", "", "Literal text to type (tmux send-keys -l)")
	namedKey := fs.String("named-key", "", "Tmux named key (e.g. BSpace, Up, C-c)")
	sendEnter := fs.Bool("enter", false, "Send an Enter keystroke")
	stream := fs.Bool("stream", false, "Read dispatch commands from stdin until EOF (#1112)")
	primary := fs.Bool("primary", false, "Target the managed first window, not the active tmux window")
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
		fmt.Println("For a Claude session, --enter and --named-key Tab/Right are withheld while")
		fmt.Println("the composer shows a prompt suggestion: Escape is pressed once and the")
		fmt.Println("command fails (delivery \"composer_blocked\") if the suggestion is still")
		fmt.Println("shown. Key names match in any case and spelling tmux accepts (enter, TAB,")
		fmt.Println("C-m, ^I, KPEnter); a --text of only CR, TAB or cursor-right is checked too.")
		fmt.Println("Type text first with --text; --stream (TUI insert mode) is unguarded.")
		fmt.Println("--enter is raw by design: it can submit unsent composer text, whatever is")
		fmt.Println("in the composer, including an operator's unsent draft.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session send-keys my-project --text \"hello\"")
		fmt.Println("  agent-deck session send-keys my-project --named-key BSpace")
		fmt.Println("  agent-deck session send-keys my-project --enter")
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	remaining := fs.Args()

	out := NewCLIOutput(*jsonOutput, *quiet)

	if len(remaining) < 1 {
		fs.Usage()
		out.Error("session id or title is required", ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Validate exactly one dispatch flag (or --stream).
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
	if *stream {
		if count != 0 {
			out.Error("--stream is mutually exclusive with --text/--named-key/--enter", ErrCodeInvalidOperation)
			exitCLI(1)
		}
	} else if count != 1 {
		out.Error("exactly one of --text, --named-key, --enter, or --stream required", ErrCodeInvalidOperation)
		exitCLI(1)
	}

	sessionRef := remaining[0]

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	inst, errMsg, errCode := ResolveSession(sessionRef, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return
	}

	if !inst.Exists() {
		out.Error(fmt.Sprintf("session '%s' is not running", inst.Title), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		out.Error(fmt.Sprintf("session '%s' has no tmux pane", inst.Title), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	var sink keySink = tmuxSess
	if *primary {
		sink = primaryWindowKeySink{session: tmuxSess}
	}

	switch {
	case *stream:
		// #1112 bug 2: persistent stream amortizes the per-keystroke ssh
		// fork+exec across an entire insert-mode session. The local TUI
		// spawns this CLI once, writes one command line per keystroke to
		// stdin, and closes stdin on insert-mode exit. Each line is one
		// of:
		//   T <hex-text>\n   (SendKeys; text is hex so newlines etc are safe)
		//   N <name>\n       (SendNamedKey; e.g. Up, BSpace, C-c)
		//   E\n              (SendEnter)
		// Blank lines and unknown verbs are ignored to keep the stream
		// resilient to forward-compatible extensions.
		if err := runSendKeysStream(os.Stdin, sink); err != nil {
			out.Error(fmt.Sprintf("send-keys stream: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	case *text != "":
		refuseIfSuggestionShown(out, inst, sink, send.LiteralTextKey(*text))
		if err := sink.SendKeys(*text); err != nil {
			out.Error(fmt.Sprintf("send-keys failed: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	case *namedKey != "":
		refuseIfSuggestionShown(out, inst, sink, *namedKey)
		if err := sink.SendNamedKey(*namedKey); err != nil {
			out.Error(fmt.Sprintf("send-named-key failed: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	case *sendEnter:
		refuseIfSuggestionShown(out, inst, sink, "Enter")
		if err := sink.SendEnter(); err != nil {
			out.Error(fmt.Sprintf("send-enter failed: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	}

	out.Success("", map[string]interface{}{
		"success":       true,
		"session_id":    inst.ID,
		"session_title": inst.Title,
	})
}

// streamKeySink is the minimum tmux-session surface runSendKeysStream
// needs. The CLI invocation uses *tmux.Session; tests can pass a fake
// that records calls.
type streamKeySink interface {
	SendKeys(text string) error
	SendNamedKey(key string) error
	SendEnter() error
}

// keySink is a streamKeySink that can also read its pane, for the prompt
// suggestion guard on single-key invocations.
type keySink interface {
	streamKeySink
	CapturePaneFresh() (string, error)
}

// suggestionEscapeSettle is send.SuggestionEscapeSettle; tests shorten it.
var suggestionEscapeSettle = send.SuggestionEscapeSettle

// guardBareKey runs before a single --enter or --named-key invocation, and
// before a --text that is only a CR, TAB or cursor-right sequence. Such a
// key has no text typed before it, so at a Claude prompt suggestion it would
// act on text nobody wrote: Tab and Right accept the suggestion, Enter
// submits from it. It presses Escape once and returns send.ErrSuggestionShown
// when the suggestion is still shown; the caller must not send the key.
//
// The --stream mode is not guarded: it is the remote half of TUI insert mode,
// a human at the keyboard, the same as the local insert mode
// (internal/ui/insert_mode.go). A human pressing Tab or Enter on a visible
// suggestion is the owner's own choice.
func guardBareKey(sink keySink, tool, key string) error {
	if !session.IsClaudeCompatible(tool) || !send.BareKeyActsOnSuggestion(key) {
		return nil
	}
	escape := func() error { return sink.SendNamedKey("Escape") }
	return send.ClearSuggestionBeforeBareKey(sink.CapturePaneFresh, escape, suggestionEscapeSettle)
}

// refuseIfSuggestionShown exits through the same error contract as a refused
// `session send` (exit 1, DELIVERY_FAILED, "delivery": "composer_blocked")
// when guardBareKey withholds the key.
func refuseIfSuggestionShown(out *CLIOutput, inst *session.Instance, sink keySink, key string) {
	err := guardBareKey(sink, inst.Tool, key)
	if err == nil {
		return
	}
	if errors.Is(err, send.ErrSuggestionShown) {
		out.ErrorWithData(fmt.Sprintf("%s not sent to '%s': %v", key, inst.Title, err), ErrCodeDeliveryFailed,
			map[string]interface{}{"delivery": deliveryComposerBlocked, "reason": "prompt_suggestion"})
		exitCLI(1)
	}
	out.Error(fmt.Sprintf("send-keys failed: %v", err), ErrCodeInvalidOperation)
	exitCLI(1)
}

type primaryWindowKeySink struct {
	session *tmux.Session
}

// CapturePaneFresh reads the managed agent window, the window the keys go to.
func (s primaryWindowKeySink) CapturePaneFresh() (string, error) {
	return s.session.CapturePrimaryFullHistory()
}

func (s primaryWindowKeySink) SendKeys(text string) error {
	return s.session.SendKeysToPrimaryWindow(text)
}

func (s primaryWindowKeySink) SendNamedKey(key string) error {
	return s.session.SendNamedKeyToPrimaryWindow(key)
}

func (s primaryWindowKeySink) SendEnter() error {
	return s.session.SendEnterToPrimaryWindow()
}

// runSendKeysStream reads dispatch commands from r and forwards each to
// sink in order. Errors on individual dispatches surface as the
// returned error so the CLI exits non-zero — partial streams are NOT
// silently dropped (a partial stream means the TUI's typed input was
// lost, which is exactly what #1112 bug 2's persistent stream is meant
// to prevent recurring with).
//
// Wire protocol (line-oriented, LF-terminated):
//
//	T <hex-text>   — SendKeys(hex.Decode(<hex-text>))
//	N <name>       — SendNamedKey(<name>)
//	E              — SendEnter()
//
// Blank lines and unknown verbs are ignored so future versions can add
// commands without breaking older `agent-deck` binaries on the remote.
func runSendKeysStream(r io.Reader, sink streamKeySink) error {
	scanner := bufio.NewScanner(r)
	// 1 MiB max line — well above any realistic insert-mode burst.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "T "):
			payload, err := hex.DecodeString(strings.TrimPrefix(line, "T "))
			if err != nil {
				return fmt.Errorf("stream: invalid hex text: %w", err)
			}
			if len(payload) == 0 {
				continue
			}
			if err := sink.SendKeys(string(payload)); err != nil {
				return fmt.Errorf("stream: SendKeys: %w", err)
			}
		case strings.HasPrefix(line, "N "):
			key := strings.TrimPrefix(line, "N ")
			if strings.TrimSpace(key) == "" {
				continue
			}
			if err := sink.SendNamedKey(key); err != nil {
				return fmt.Errorf("stream: SendNamedKey(%q): %w", key, err)
			}
		case line == "E":
			if err := sink.SendEnter(); err != nil {
				return fmt.Errorf("stream: SendEnter: %w", err)
			}
		default:
			// Unknown verb — ignore for forward compatibility.
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("stream: read: %w", err)
	}
	return nil
}
