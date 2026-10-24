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
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

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
	out, err := r.runWithStdinFor(ctx, imageUploadTimeout, data, "session", "image-upload", sessionID, "--name", name, "--json")
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
	if jsonErr := json.Unmarshal(bytes.TrimSpace(out), &receipt); jsonErr != nil || !strings.HasPrefix(receipt.Path, "/") {
		return "", fmt.Errorf("image upload: unexpected answer from the remote")
	}
	if why := untypeableReason(receipt.Path); why != "" {
		return "", fmt.Errorf("image upload: host path %s %s", strconv.QuoteToGraphic(receipt.Path), why)
	}
	return receipt.Path, nil
}

// untypeableReason says why a host path cannot be typed (typeablePath), ""
// when it can.
func untypeableReason(path string) string {
	switch {
	case path == "":
		return "is empty"
	case !utf8.ValidString(path):
		return "is not valid UTF-8, which a terminal could read as a control sequence"
	case strings.ContainsFunc(path, isControl):
		return "has a control character, which would act in the remote terminal"
	case strings.ContainsFunc(path, unicode.IsSpace):
		return "has a space, which would end the @mention there (the remote's home or profile directory has one)"
	case strings.ContainsFunc(path, func(r rune) bool { return unicode.Is(unicode.Cf, r) }):
		return "has an invisible format character, which would show a different path than the one typed"
	}
	return ""
}

// typeablePath reports whether a host path can be typed into the remote
// program as it is: valid UTF-8 (a stray 0x9b is CSI to an 8-bit reader), no
// control character (an escape sequence, a bracketed-paste end or a newline
// would act there), no white space (it would end the @mention) and no
// invisible format character (a bidi override or zero-width character would
// show the owner a different path than the one typed).
func typeablePath(path string) bool { return untypeableReason(path) == "" }

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
// upload (one ssh round trip on the attach's master connection). With the
// clipboard read (clipboardReadTimeout) a Ctrl+V over an image holds the
// input for at most 3.5 s; without an image only the clipboard read runs.
const pasteTargetTimeout = 2 * time.Second

// clipboardReadTimeout bounds the one clipboard read per Ctrl+V.
const clipboardReadTimeout = 1500 * time.Millisecond

// imageUploadTimeout is the upload's own deadline: a 20 MiB image over a
// slow link outlasts the 30 s every other remote command gets.
const imageUploadTimeout = 120 * time.Second

// remoteImagePaste handles a lone Ctrl+V in one remote attach.
type remoteImagePaste struct {
	remote string
	// readImage reads this machine's clipboard image once (clipboardImage).
	readImage func() clipboardRead
	upload    func(ctx context.Context, data []byte, ext string) (string, error)
	// target returns the session's harness and its pane now: the key is taken
	// only when that harness reads a pasted @path and its composer is on
	// screen (tmux.AgentComposerVisible), never in an editor or a shell.
	target func(ctx context.Context) (tool, pane string, err error)
	// write types into the remote session (the attach's input side).
	write func([]byte) error
	// notice shows one line to the owner until the next one; "" clears it.
	notice func(string)
	// alert shows a line that says why the image did not go (too large, a
	// failed upload) and rings the bell; nil falls back to notice.
	alert func(string)
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
func pasteClipboardSlowNotice() string {
	return "agent-deck: the clipboard took too long to read (a very large image?)"
}
func pasteTooOldNotice(remote string) string {
	return "agent-deck: " + remote + "'s agent-deck is too old for image paste (agent-deck remote update " + remote + ")"
}
func pasteFailedNotice(remote string, err error) string {
	return "agent-deck: the image could not reach " + remote + ": " + oneLine(errText(err), 80)
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(stripControls(s)), " ")
	if r := []rune(s); len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}

// isControl is a C0 or C1 control character or DEL: each can start or end a
// terminal sequence.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) }

// stripControls turns control characters into spaces and drops invalid
// UTF-8 (a stray byte such as 0x9b is CSI to an 8-bit terminal).
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
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
	busy := p.busy
	p.mu.Unlock()
	if busy {
		return false // one upload at a time; a Ctrl+V meanwhile reaches the program as typed
	}
	clip := p.readImage()
	if len(clip.data) == 0 && !clip.tooLarge && !clip.timedOut {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), pasteTargetTimeout)
	tool, pane, err := p.target(ctx)
	cancel()
	agent := pasteAgent(tool)
	if err != nil || agent == "" || !tmux.AgentComposerVisible(agent, pane) {
		return false // not an agent's composer, or it cannot be told: the key goes on as typed
	}
	if clip.timedOut {
		p.alertf(pasteClipboardSlowNotice())
		return true
	}
	if clip.tooLarge || int64(len(clip.data)) > MaxImageUploadBytes {
		p.alertf(pasteTooLargeNotice())
		return true
	}
	p.mu.Lock()
	p.busy = true
	p.mu.Unlock()
	p.notice(pasteUploadingNotice(p.remote))
	go p.send(clip.data, clip.ext, agent)
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
		timeout = imageUploadTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	path, err := p.upload(ctx, image, ext)
	switch {
	case errors.Is(err, ErrImageUploadUnsupported):
		p.alertf(pasteTooOldNotice(p.remote))
		return
	case err != nil:
		p.alertf(pasteFailedNotice(p.remote, err))
		return
	}
	text := []byte(pastedPaths([]string{path}, agent))
	if p.bracketed != nil && p.bracketed() {
		text = append(append([]byte("\x1b[200~"), text...), []byte("\x1b[201~")...)
	}
	p.notice("")
	_ = p.write(text)
}

func (p *remoteImagePaste) alertf(text string) {
	if p.alert != nil {
		p.alert(text)
		return
	}
	p.notice(text)
}

// clipboardRead is one read of this machine's clipboard: an image and its
// file extension, nothing, an image over MaxImageUploadBytes (tooLarge), or
// a reader that ran out of time (timedOut: most likely a very large image,
// which osascript hex-encodes, so not taken for "no image").
type clipboardRead struct {
	data     []byte
	ext      string
	tooLarge bool
	timedOut bool
}

// osascriptClipboardImage asks for the clipboard as PNG, then as JPEG, in one
// osascript run; it answers «data PNGf…», «data JPEG…» or "".
var osascriptClipboardImage = []string{
	"-e", "try", "-e", "return the clipboard as «class PNGf»", "-e", "end try",
	"-e", "try", "-e", "return the clipboard as «class JPEG»", "-e", "end try",
	"-e", `return ""`,
}

// clipboardImage reads this machine's clipboard image once: macOS through one
// osascript run (no install), Linux through wl-paste or xclip when present.
func clipboardImage() clipboardRead {
	switch runtime.GOOS {
	case "darwin":
		// osascript answers in hex: twice the bytes, plus the «data PNGf…» frame.
		out, status := boundedOutput(clipboardReadTimeout, 2*MaxImageUploadBytes+64, "osascript", osascriptClipboardImage...)
		switch status {
		case clipOverCap:
			return clipboardRead{tooLarge: true}
		case clipTimedOut:
			return clipboardRead{timedOut: true}
		}
		for _, kind := range []struct{ class, ext string }{{"PNGf", "png"}, {"JPEG", "jpg"}} {
			if data := parseOSAScriptData(out, kind.class); len(data) > 0 {
				return clipboardRead{data: data, ext: kind.ext}
			}
		}
	case "linux":
		readers := [][]string{}
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			readers = append(readers, []string{"wl-paste", "--no-newline", "--type", "image/png"})
		}
		if os.Getenv("DISPLAY") != "" {
			readers = append(readers, []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"})
		}
		for _, reader := range readers {
			data, status := boundedOutput(clipboardReadTimeout, MaxImageUploadBytes, reader[0], reader[1:]...)
			switch status {
			case clipOverCap:
				return clipboardRead{tooLarge: true}
			case clipTimedOut:
				return clipboardRead{timedOut: true}
			}
			if isPNG(data) {
				return clipboardRead{data: data, ext: "png"}
			}
		}
	}
	return clipboardRead{}
}

func isPNG(data []byte) bool { return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) }

// clipStatus is how one clipboard reader run ended.
type clipStatus int

const (
	clipNone     clipStatus = iota // no reader, or it failed: no image
	clipRead                       // the reader answered (data may still be no image)
	clipOverCap                    // the answer was over the cap
	clipTimedOut                   // the reader ran out of time
)

// boundedOutput runs a clipboard reader with a deadline and reads at most
// limit bytes of its answer. An answer over the cap stops the reader at once
// (clipOverCap) and a reader past its deadline is killed (clipTimedOut); in
// both cases the data is dropped and the caller reports it rather than
// taking it for "no image".
func boundedOutput(timeout time.Duration, limit int64, name string, args ...string) ([]byte, clipStatus) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, clipNone
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- fixed reader programs and arguments
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, clipNone
	}
	if err := cmd.Start(); err != nil {
		return nil, clipNone
	}
	data, _ := io.ReadAll(io.LimitReader(stdout, limit+1))
	if int64(len(data)) > limit {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, clipOverCap
	}
	if err := cmd.Wait(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, clipTimedOut
		}
		return nil, clipNone
	}
	return data, clipRead
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
