package tmux

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Attach notices: a one-line message from code running beside an attach
// (the TUI's attach worker) to the person in it. Whatever attach is live
// registers where such a line goes: a local attach the status line of its
// own tmux client, a remote attach (internal/session) the last row of the
// terminal. Nothing is shown when no attach is running.

type attachNoticeSink struct{ show func(string) }

var attachNotice struct {
	mu   sync.Mutex
	sink *attachNoticeSink
}

// SetAttachNoticeSink makes show the target of ShowAttachNotice for the
// attach that is starting; the returned func removes it again (and leaves
// a later attach's sink alone).
func SetAttachNoticeSink(show func(string)) (clear func()) {
	s := &attachNoticeSink{show: show}
	attachNotice.mu.Lock()
	attachNotice.sink = s
	attachNotice.mu.Unlock()
	return func() {
		attachNotice.mu.Lock()
		if attachNotice.sink == s {
			attachNotice.sink = nil
		}
		attachNotice.mu.Unlock()
	}
}

// ShowAttachNotice shows text in the live attach and reports whether there
// was one.
func ShowAttachNotice(text string) bool {
	attachNotice.mu.Lock()
	s := attachNotice.sink
	attachNotice.mu.Unlock()
	if s == nil {
		return false
	}
	s.show(text)
	return true
}

// attachNoticeDelay is how long an attach notice stays on a local client's
// status line.
const attachNoticeDelay = 15 * time.Second

// showClientNotice puts msg on the status line of the tmux client whose
// process is pid, i.e. the client this process attached, and no other.
// All clients of the server are listed, not only s's: the notification
// bar can switch our client to another session during the attach.
func (s *Session) showClientNotice(ctx context.Context, pid int, msg string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := commandOutput(s.tmuxCmdContext(ctx, "list-clients", "-F", "#{client_pid}\t#{client_name}"))
	if err != nil {
		return
	}
	want := strconv.Itoa(pid)
	for _, line := range strings.Split(string(out), "\n") {
		if p, name, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok && p == want {
			s.showNoticeFor(ctx, Viewer{Name: name}, msg, attachNoticeDelay, "attach_notice")
			return
		}
	}
}
