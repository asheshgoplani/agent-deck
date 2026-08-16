// Package desknotify raises a desktop notification when a managed session
// needs the operator's attention.
//
// The gap it fills: agent-deck already detects running -> waiting/error/idle
// transitions and dispatches them through TransitionNotifier, but that path is
// PARENT-keyed. It routes a child's transition to the parent session's inbox,
// so a TOP-LEVEL session (no ParentSessionID) reaches nobody. The notification
// bar in the tmux status line is the only other signal, and it is only visible
// while you are looking at the agent-deck TUI. A background agent that blocks
// on a permission prompt while you work elsewhere therefore surfaces nowhere.
//
// This package is the operator-facing counterpart: it notifies the HUMAN, not
// a parent agent. It is deliberately a leaf with no internal dependencies so
// both internal/session and cmd/agent-deck can use it without an import cycle
// (same constraint that shaped internal/childenv).
//
// Delivery is best-effort by design. A notifier that can fail a status poll,
// block a daemon tick, or spam one banner per poll would be worse than no
// notifier at all, so every failure mode degrades to silence:
//
//   - No notifier binary on PATH: no-op, no error.
//   - Notifier exits non-zero or hangs: bounded by a timeout, result ignored.
//   - Same session still waiting on the next poll: suppressed by the caller's
//     transition edge (running -> waiting fires once, not every tick).
package desknotify

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// dispatchTimeout bounds a single notifier invocation. The status-poll loop
// calls into here, so a wedged notifier must not stall session monitoring.
// cmux's own CLI talks to a local Unix socket and returns in single-digit
// milliseconds; 3s is generous enough that a loaded machine still delivers,
// and short enough that a hung binary costs one tick rather than the session.
const dispatchTimeout = 3 * time.Second

// Backend is a desktop notification transport.
type Backend interface {
	// Name identifies the backend in logs.
	Name() string
	// Available reports whether this backend can deliver right now. It must be
	// cheap: it is consulted on every notification.
	Available() bool
	// Notify delivers title/body. Errors are advisory; callers ignore them.
	Notify(ctx context.Context, title, body string) error
}

// Notification describes an attention-worthy session state change.
type Notification struct {
	// SessionTitle is the human-facing session name ("flow", "natera").
	SessionTitle string
	// Profile is the agent-deck profile the session belongs to. Included in the
	// body because the same title can exist in more than one profile.
	Profile string
	// ToStatus is the status just entered: waiting, error, or idle.
	ToStatus string
}

// Title renders the notification title. Kept short: macOS truncates a banner
// title aggressively, and the session name is the part that identifies which
// agent needs you.
func (n Notification) Title() string {
	title := strings.TrimSpace(n.SessionTitle)
	if title == "" {
		return "agent-deck"
	}
	return title
}

// Body renders the notification body, leading with what the operator must do.
// "needs input" rather than "status changed to waiting": the status name is
// agent-deck's vocabulary, not a description of the action required.
func (n Notification) Body() string {
	var action string
	switch strings.ToLower(strings.TrimSpace(n.ToStatus)) {
	case "waiting":
		action = "needs input"
	case "error":
		action = "hit an error"
	case "idle":
		action = "finished"
	default:
		// Unknown status: report it verbatim rather than inventing a phrasing.
		// Callers gate on a known set, so this is a defensive branch.
		action = "is " + strings.TrimSpace(n.ToStatus)
	}
	if p := strings.TrimSpace(n.Profile); p != "" && p != "default" {
		return action + " (profile " + p + ")"
	}
	return action
}

// cmuxBackend delivers through the cmux terminal's notification panel, which
// also raises a macOS banner. Preferred over a raw AppleScript notification
// because cmux records the notification in its sidebar, so an alert missed
// while away is still discoverable.
//
// This does NOT target a specific cmux surface. A managed session lives in a
// detached tmux session with no fixed surface: it may be viewed from several
// surfaces over its life, or none, so a surface id captured at spawn time
// would be stale or absent exactly when a background agent needs attention.
// Workspace-level delivery is the honest granularity here.
type cmuxBackend struct{}

func (cmuxBackend) Name() string { return "cmux" }

func (cmuxBackend) Available() bool {
	_, err := exec.LookPath("cmux")
	return err == nil
}

func (cmuxBackend) Notify(ctx context.Context, title, body string) error {
	// #nosec G204 -- title/body are session metadata passed as separate argv
	// elements, never interpolated into a shell string. No shell is involved.
	return exec.CommandContext(ctx, "cmux", "notify",
		"--title", deflagged(title),
		"--subtitle", "agent-deck",
		"--body", deflagged(body),
	).Run()
}

// deflagged neutralizes a value that a CLI would read as a flag rather than as
// the argument it follows.
//
// This is not a command-injection guard: the values go through argv, so no
// shell parses them, and a flag-looking title like "--surface" is safely bound
// as the value of the preceding --title. The one case that misbehaves is a
// value of exactly "-h" or "--help", which makes the notifier print usage and
// deliver nothing. Losing the alert for a session titled "-h" is silly rather
// than dangerous, but it is silent, which is the failure mode this whole
// feature exists to remove.
func deflagged(s string) string {
	switch strings.TrimSpace(s) {
	case "-h", "--help":
		// A zero-width space keeps the rendered text identical while stopping
		// the argument from matching the notifier's help flag exactly.
		return s + "​"
	default:
		return s
	}
}

// notifySendBackend is the Linux fallback for a terminal that is not cmux.
// It raises a desktop notification through the freedesktop.org notification
// service when the notify-send client is installed.
type notifySendBackend struct{}

func (notifySendBackend) Name() string { return "notify-send" }

func (notifySendBackend) Available() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := exec.LookPath("notify-send")
	return err == nil
}

func (notifySendBackend) Notify(ctx context.Context, title, body string) error {
	// #nosec G204 -- title/body are separate argv elements and no shell parses
	// them. -- ends option parsing, while deflagged preserves the same defense
	// in depth used by the cmux backend for exact help flags.
	return exec.CommandContext(ctx, "notify-send", "--", deflagged(title), deflagged(body)).Run()
}

// osascriptBackend is the macOS fallback for a terminal that is not cmux. It
// raises a Notification Center banner with no persistent record.
type osascriptBackend struct{}

func (osascriptBackend) Name() string { return "osascript" }

func (osascriptBackend) Available() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath("osascript")
	return err == nil
}

func (osascriptBackend) Notify(ctx context.Context, title, body string) error {
	// AppleScript has no argv, so title/body MUST be embedded in the script
	// source. Escape backslashes first, then quotes: reversing the order would
	// re-escape the backslashes this step introduces. A session titled
	// `foo" & (do shell script "id") & "` would otherwise execute.
	script := `display notification "` + escapeAppleScript(body) +
		`" with title "` + escapeAppleScript(title) + `"`
	// #nosec G204 -- the only interpolated values are escaped above, and the
	// argv form means no shell parses this string.
	return exec.CommandContext(ctx, "osascript", "-e", script).Run()
}

// escapeAppleScript makes s safe to embed in an AppleScript string literal.
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// Notifier dispatches notifications to the first available backend.
type Notifier struct {
	backends []Backend
}

// New returns a Notifier preferring cmux, then platform-native notifications.
func New() *Notifier {
	return &Notifier{backends: []Backend{cmuxBackend{}, notifySendBackend{}, osascriptBackend{}}}
}

// NewWithBackends builds a Notifier over an explicit backend list. Tests use
// this to assert dispatch and ordering without spawning a real notifier.
func NewWithBackends(backends ...Backend) *Notifier {
	return &Notifier{backends: backends}
}

// Notify delivers n through the first available backend and reports which one
// handled it, or "" when none was available or delivery failed.
//
// Never returns an error: no caller should change behaviour based on whether a
// banner appeared, and a status poll must not fail because a notifier did.
func (nt *Notifier) Notify(n Notification) string {
	if nt == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), dispatchTimeout)
	defer cancel()

	for _, b := range nt.backends {
		if b == nil || !b.Available() {
			continue
		}
		if err := b.Notify(ctx, n.Title(), n.Body()); err != nil {
			// Try the next backend: an available-but-failing transport (cmux
			// installed while the app is closed) should still fall through to
			// the OS notifier rather than swallow the alert.
			continue
		}
		return b.Name()
	}
	return ""
}

// ShouldNotify reports whether a status merits interrupting the operator.
//
// Deliberately narrower than the set the parent-routing path uses. "idle" is
// excluded: for a long-lived interactive agent it is the resting state, not an
// event, and notifying on it would train the operator to ignore the banners.
// A session that stops needing you is not a session that needs you.
func ShouldNotify(toStatus string) bool {
	switch strings.ToLower(strings.TrimSpace(toStatus)) {
	case "waiting", "error":
		return true
	default:
		return false
	}
}
