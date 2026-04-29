package session

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// claudeResumePickerCapturer is the minimal pane-read surface needed by the
// auto-handler. *tmux.Session satisfies it via CapturePaneFresh().
type claudeResumePickerCapturer interface {
	CapturePaneFresh() (string, error)
}

// claudeResumePickerSender is the minimal key-write surface needed by the
// auto-handler. *tmux.Session satisfies it via SendEnter().
type claudeResumePickerSender interface {
	SendEnter() error
}

// autoResumeOptions tunes how aggressively we sample the pane after spawn.
type autoResumeOptions struct {
	PollInterval time.Duration
	Timeout      time.Duration
}

// ansiSGR strips Select Graphic Rendition escape sequences (\x1b[...m) so
// marker matching is not derailed by colour codes that the -e capture flag
// preserves.
var ansiSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// resumePickerMarkers are substrings that appear together only on Claude's
// "Resume from summary / Resume full session" picker. Both must be present
// to count as a match — that rules out a chat message that happens to mention
// one phrase in isolation.
var resumePickerMarkers = []string{
	"Resume from summary",
	"Resume full session",
}

// looksLikeClaudeResumePicker reports whether the pane text shows the picker.
func looksLikeClaudeResumePicker(pane string) bool {
	clean := ansiSGR.ReplaceAllString(pane, "")
	for _, m := range resumePickerMarkers {
		if !strings.Contains(clean, m) {
			return false
		}
	}
	return true
}

// autoResolveClaudeResumePicker polls the pane until the picker is visible or
// the timeout elapses. When seen, it sends Enter exactly once (the default
// option, "Resume from summary", is preselected) and returns resolved=true.
//
// If the picker never appears within the budget, returns (false, nil) — the
// common case for fresh sessions and small conversations.
//
// Capture errors propagate immediately so the caller can log and move on
// rather than the conductor sitting frozen.
func autoResolveClaudeResumePicker(
	ctx context.Context,
	cap claudeResumePickerCapturer,
	send claudeResumePickerSender,
	opts autoResumeOptions,
) (bool, error) {
	if opts.PollInterval <= 0 {
		opts.PollInterval = 100 * time.Millisecond
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}

	deadline := time.Now().Add(opts.Timeout)
	for {
		pane, err := cap.CapturePaneFresh()
		if err != nil {
			return false, err
		}
		if looksLikeClaudeResumePicker(pane) {
			if sErr := send.SendEnter(); sErr != nil {
				return false, sErr
			}
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(opts.PollInterval):
		}
	}
}
