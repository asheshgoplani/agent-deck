package session

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Image paste into a remote attach (owner report 2026-10-18). In a terminal
// attached to a remote session, Ctrl+V reaches the harness on the host, and
// Claude Code there reads the host's clipboard, never the one on the machine
// the owner copied the screenshot on, so the image could never arrive. The
// attach (agent-deck attach, the TUI's Enter; ssh or mosh) is the one place
// that sees both sides: on a lone Ctrl+V with an image on this machine's
// clipboard it sends the image up through the remote's own
// `session image-upload` (the Mac app's route, into the session's
// macapp-uploads folder there) and types the host path the way the
// composer's --image does: `@<path>` for Claude and Gemini, the bare path for
// other harnesses. Text pastes and a Ctrl+V with no image are untouched.
//
// The key is taken only when the session's harness reads a pasted @path
// (Claude-compatible, Gemini) and its composer is on screen; in an editor, a
// shell, a dialog, or when that cannot be told, Ctrl+V goes on as typed.
// `[remote] paste_images = false` turns the whole route off.
//
// A plain `ssh host` + `tmux attach` without agent-deck has no such hop: the
// keypress goes straight to the host and the image cannot arrive. That path
// is documented, not handled.

// ErrImageUploadUnsupported is a remote whose agent-deck predates
// `session image-upload`.
var ErrImageUploadUnsupported = errors.New("remote agent-deck has no session image-upload")

// UploadImage stages data on the remote for one of its sessions and returns
// the host path (`session image-upload <id> --name <uuid>.<ext> --json`,
// bytes on stdin; the remote refuses over MaxImageUploadBytes).
func (r *SSHRunner) UploadImage(ctx context.Context, sessionID string, data []byte, ext string) (string, error) {
	name := uuid.NewString() + "." + ext
	out, err := r.runWithStdin(ctx, data, "session", "image-upload", sessionID, "--name", name, "--json")
	if text := strings.ToLower(string(out) + " " + errText(err)); strings.Contains(text, "unknown command") ||
		strings.Contains(text, "unknown session command") || strings.Contains(text, "unsupported remote command") {
		return "", ErrImageUploadUnsupported
	}
	if err != nil {
		return "", err
	}
	var receipt struct {
		Path string `json:"path"`
	}
	if jsonErr := json.Unmarshal(bytes.TrimSpace(out), &receipt); jsonErr != nil || !strings.HasPrefix(receipt.Path, "/") ||
		strings.ContainsAny(receipt.Path, "\n\x00") {
		return "", fmt.Errorf("image upload: unexpected answer from the remote")
	}
	return receipt.Path, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// PasteTarget asks the remote for a session's harness and its pane as shown
// now (`session output <id> --pane --json`, one call).
func (r *SSHRunner) PasteTarget(ctx context.Context, sessionID string) (tool, pane string, err error) {
	out, err := r.Run(ctx, "session", "output", sessionID, "--pane", "--json")
	if err != nil {
		return "", "", err
	}
	var shown struct {
		Tool    string `json:"tool"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &shown); err != nil {
		return "", "", fmt.Errorf("paste target: unexpected answer from the remote")
	}
	return shown.Tool, shown.Content, nil
}

// pasteAgent is the harness a pasted @path is typed for: Claude-compatible
// tools and Gemini read it; every other tool (a shell, Codex, an editor
// session) never gets its Ctrl+V taken. "" for those.
func pasteAgent(tool string) string {
	switch {
	case IsClaudeCompatible(tool):
		return "claude"
	case strings.EqualFold(tool, "gemini"):
		return "gemini"
	}
	return ""
}

// pasteTargetTimeout bounds the check that runs between the key and the
// upload (one ssh round trip on the attach's master connection).
const pasteTargetTimeout = 3 * time.Second

// remoteImagePaste handles a lone Ctrl+V in one remote attach.
type remoteImagePaste struct {
	remote string
	// readImage returns this machine's clipboard image and its file extension
	// (png or jpg); nil data when the clipboard holds none.
	readImage func() ([]byte, string)
	upload    func(ctx context.Context, data []byte, ext string) (string, error)
	// target returns the session's harness and its pane now: the key is taken
	// only when that harness reads a pasted @path and its composer is on
	// screen (tmux.AgentComposerVisible), never in an editor or a shell.
	target func(ctx context.Context) (tool, pane string, err error)
	// write types into the remote session (the attach's input side).
	write func([]byte) error
	// notice shows one line to the owner; "" clears it.
	notice func(string)
	// bracketed reports whether the remote program asked for bracketed paste.
	bracketed func() bool
	timeout   time.Duration

	mu   sync.Mutex
	busy bool
}

// Notices shown in the attach while an image goes up, or why it did not.
func pasteUploadingNotice(remote string) string {
	return "agent-deck: uploading the clipboard image to " + remote + "…"
}
func pasteTooLargeNotice() string {
	return "agent-deck: images over 20 MiB cannot be pasted"
}
func pasteTooOldNotice(remote string) string {
	return "agent-deck: " + remote + "'s agent-deck is too old for image paste (agent-deck remote update " + remote + ")"
}
func pasteFailedNotice(remote string, err error) string {
	return "agent-deck: the image could not reach " + remote + ": " + oneLine(errText(err), 80)
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}

// pastedPaths is what gets typed for uploaded host paths.
func pastedPaths(paths []string, tool string) string {
	mention := IsClaudeCompatible(tool) || strings.EqualFold(tool, "gemini")
	parts := make([]string, len(paths))
	for i, p := range paths {
		if mention {
			p = "@" + p
		}
		parts[i] = p
	}
	return strings.Join(parts, " ") + " "
}

// take reports whether data (one read of the owner's keys) is a Ctrl+V this
// paste handles; false leaves it to be forwarded as it is.
func (p *remoteImagePaste) take(data []byte) bool {
	if p == nil || len(data) != 1 || data[0] != 0x16 {
		return false
	}
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		return true // one upload at a time; a second Ctrl+V meanwhile is dropped, not sent as a key
	}
	p.mu.Unlock()
	image, ext := p.readImage()
	if len(image) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), pasteTargetTimeout)
	tool, pane, err := p.target(ctx)
	cancel()
	agent := pasteAgent(tool)
	if err != nil || agent == "" || !tmux.AgentComposerVisible(agent, pane) {
		return false // not an agent's composer, or it cannot be told: the key goes on as typed
	}
	if int64(len(image)) > MaxImageUploadBytes {
		p.notice(pasteTooLargeNotice())
		return true
	}
	p.mu.Lock()
	p.busy = true
	p.mu.Unlock()
	p.notice(pasteUploadingNotice(p.remote))
	go p.send(image, ext, agent)
	return true
}

func (p *remoteImagePaste) send(image []byte, ext, agent string) {
	defer func() {
		p.mu.Lock()
		p.busy = false
		p.mu.Unlock()
	}()
	timeout := p.timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	path, err := p.upload(ctx, image, ext)
	switch {
	case errors.Is(err, ErrImageUploadUnsupported):
		p.notice(pasteTooOldNotice(p.remote))
		return
	case err != nil:
		p.notice(pasteFailedNotice(p.remote, err))
		return
	}
	text := []byte(pastedPaths([]string{path}, agent))
	if p.bracketed != nil && p.bracketed() {
		text = append(append([]byte("\x1b[200~"), text...), []byte("\x1b[201~")...)
	}
	p.notice("")
	_ = p.write(text)
}

// clipboardImage reads this machine's clipboard image: macOS through
// osascript (no install), Linux through wl-paste or xclip when present.
func clipboardImage() ([]byte, string) {
	switch runtime.GOOS {
	case "darwin":
		for _, kind := range []struct{ class, ext string }{{"PNGf", "png"}, {"JPEG", "jpg"}} {
			out := boundedOutput(2*time.Second, "osascript", "-e", "the clipboard as «class "+kind.class+"»")
			if data := parseOSAScriptData(out, kind.class); len(data) > 0 {
				return data, kind.ext
			}
		}
	case "linux":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			if data := boundedOutput(2*time.Second, "wl-paste", "--no-newline", "--type", "image/png"); isPNG(data) {
				return data, "png"
			}
		}
		if os.Getenv("DISPLAY") != "" {
			if data := boundedOutput(2*time.Second, "xclip", "-selection", "clipboard", "-t", "image/png", "-o"); isPNG(data) {
				return data, "png"
			}
		}
	}
	return nil, ""
}

func isPNG(data []byte) bool { return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) }

// boundedOutput runs a clipboard reader with a deadline and a size cap
// (hex doubles osascript's answer), nil when it is missing or fails.
func boundedOutput(timeout time.Duration, name string, args ...string) []byte {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- fixed reader programs and arguments
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(stdout, 2*MaxImageUploadBytes+64))
	if err := cmd.Wait(); err != nil {
		return nil
	}
	return data
}

// parseOSAScriptData decodes osascript's «data PNGf89504E47…» answer.
func parseOSAScriptData(out []byte, class string) []byte {
	text := strings.TrimSpace(string(out))
	prefix := "«data " + class
	if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, "»") {
		return nil
	}
	data, err := hex.DecodeString(strings.TrimSuffix(strings.TrimPrefix(text, prefix), "»"))
	if err != nil {
		return nil
	}
	return data
}

// bracketedPasteTracker follows the remote program's bracketed-paste mode in
// the attach's output (CSI ? 2004 h / l), across reads.
type bracketedPasteTracker struct {
	mu   sync.Mutex
	on   bool
	tail []byte
}

var (
	bracketedOn  = []byte("\x1b[?2004h")
	bracketedOff = []byte("\x1b[?2004l")
)

func (t *bracketedPasteTracker) observe(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	buf := append(append([]byte(nil), t.tail...), p...)
	on, off := bytes.LastIndex(buf, bracketedOn), bytes.LastIndex(buf, bracketedOff)
	if on >= 0 || off >= 0 {
		t.on = on > off
	}
	keep := len(bracketedOn) - 1
	if len(buf) < keep {
		keep = len(buf)
	}
	t.tail = append(t.tail[:0], buf[len(buf)-keep:]...)
}

func (t *bracketedPasteTracker) enabled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.on
}
