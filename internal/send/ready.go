package send

import (
	"fmt"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// DefaultAgentReadyTimeout is the readiness budget for launch / StartWithMessage.
// Matches `session send --timeout` default so cold-start TUIs (Cursor, Claude+MCP)
// are not cut off early.
const DefaultAgentReadyTimeout = 10 * time.Minute

// startupPromptConfirmations is how many consecutive polls must see the tool
// prompt before the startup-window bypass below accepts it.
//
// A prompt drawn during the startup window is not proof the tool will keep
// what we type next. Claude Code paints its composer box early and then keeps
// mounting - MCP servers connecting, hooks firing, the remote-control bridge
// dialing - and keystrokes delivered into that window can be dropped as the
// input component remounts. Because the send is a single tmux send-keys of the
// whole message, what survives is a *tail*: the caller then presses Enter on a
// fragment, the agent answers the fragment, and the session looks entirely
// healthy afterwards. verifyPromptConsumedAfterLaunch cannot catch it either -
// it polls until the composer is rendered and holds no draft, i.e. until
// *something* was submitted, and never compares what landed against what was
// sent, so a submitted fragment reads as a clean delivery.
//
// Observed on a Claude launch carrying a 2183-byte --message-file: 88 bytes
// arrived, the closing paragraph, and the session sat idle for 18 hours.
//
// One sighting was the old bar. Requiring the prompt to still be there three
// polls later costs 400ms on a launch and takes the bypass out of the window
// where the pane is still being repainted. It cannot deadlock: a prompt that
// is really up stays up, and the surrounding timeout is unchanged.
const startupPromptConfirmations = 3

// AgentReadyChecker abstracts the tmux surface readiness polling needs.
// *tmux.Session satisfies this interface.
type AgentReadyChecker interface {
	GetStatus() (string, error)
	CapturePaneFresh() (string, error)
}

// PromptGates enables tool-specific prompt visibility checks once GetStatus
// reports waiting/idle (or during the startup-window prompt probe below).
type PromptGates struct {
	ClaudeComposer bool // IsClaudeCompatible tools: require ❯ composer line
	CodexPrompt    bool // IsCodexCompatible tools: require codex>/› prompt
}

// WaitForAgentReady waits until the agent accepts keyboard input.
//
// Primary path: GetStatus active → waiting/idle transition (same as the TUI).
// Secondary path (#cursor-launch): during tmux's startup window GetStatus can
// return "starting" without re-capturing the pane when window_activity is flat,
// even though the tool prompt is already on screen and the user can type.
// When status is "starting", probe CapturePaneFresh directly for a tool prompt.
func WaitForAgentReady(target AgentReadyChecker, tool string, timeout time.Duration, gates PromptGates) error {
	const pollInterval = 200 * time.Millisecond
	if timeout <= 0 {
		timeout = DefaultAgentReadyTimeout
	}
	maxAttempts := int(timeout / pollInterval)
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	sawActive := false
	readyCount := 0
	startupPromptSeen := 0

	for attempt := 0; attempt < maxAttempts; attempt++ {
		time.Sleep(pollInterval)

		status, err := target.GetStatus()
		if err != nil {
			readyCount = 0
			continue
		}

		// Startup-window bypass: prompt visible in pane but GetStatus still
		// "starting" because prompt detection only runs on activity changes.
		if status == "starting" {
			if paneShowsReadyPrompt(target, tool, gates) {
				startupPromptSeen++
				if startupPromptSeen >= startupPromptConfirmations {
					time.Sleep(300 * time.Millisecond)
					return nil
				}
				readyCount = 0
				continue
			}
			// A prompt that came and went was a transient paint, not a
			// tool waiting for input. Start counting again.
			startupPromptSeen = 0
			readyCount = 0
			continue
		}

		if status == "active" {
			sawActive = true
			readyCount = 0
			continue
		}

		if status == "waiting" || status == "idle" {
			readyCount++
		} else {
			readyCount = 0
		}

		alreadyReady := readyCount >= 10 && attempt >= 15
		if (sawActive && (status == "waiting" || status == "idle")) || alreadyReady {
			if gates.ClaudeComposer {
				if rawContent, captureErr := target.CapturePaneFresh(); captureErr == nil && !HasCurrentComposerPrompt(tmux.StripANSI(rawContent)) {
					continue
				}
			}
			if gates.CodexPrompt {
				if rawContent, captureErr := target.CapturePaneFresh(); captureErr == nil {
					content := tmux.StripANSI(rawContent)
					detector := tmux.NewPromptDetector("codex")
					if !detector.HasPrompt(content) {
						continue
					}
				}
			}
			if tool == "cursor" {
				if rawContent, captureErr := target.CapturePaneFresh(); captureErr == nil {
					content := tmux.StripANSI(rawContent)
					detector := tmux.NewPromptDetector("cursor")
					if !detector.HasPrompt(content) {
						continue
					}
				}
			}
			time.Sleep(300 * time.Millisecond)
			return nil
		}
	}

	return fmt.Errorf("agent not ready after %s", timeout)
}

func paneShowsReadyPrompt(target AgentReadyChecker, tool string, gates PromptGates) bool {
	raw, err := target.CapturePaneFresh()
	if err != nil {
		return false
	}
	content := tmux.StripANSI(raw)
	if paneLooksBusy(content) {
		return false
	}
	if gates.ClaudeComposer {
		return HasCurrentComposerPrompt(content)
	}
	if gates.CodexPrompt {
		return tmux.NewPromptDetector("codex").HasPrompt(content)
	}
	return tmux.NewPromptDetector(tool).HasPrompt(content)
}

func paneLooksBusy(content string) bool {
	lower := strings.ToLower(content)
	return strings.Contains(lower, "ctrl+c to interrupt") ||
		strings.Contains(lower, "esc to interrupt")
}
