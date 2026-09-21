package tmux

import (
	"log/slog"
	"strconv"
	"strings"
	"syscall"
)

// Shared view: agent-deck's control clients and the window's latest client.
//
// `window-size latest` sizes a window to its *latest* client. tmux never sizes
// a window from a control-mode client that has not asked for a size (resize.c
// ignore_client_size), but it does not keep such a client out of the latest
// slot (tmux 3.3a to 3.6a):
//
//   - attaching makes the attaching client the window's latest client,
//     control clients included (server-client.c server_client_set_session);
//   - when the latest client leaves, tmux hands the slot to the most recently
//     active client left on the window (server_client_attached_lost). A
//     control client's activity is its attach time, and the PipeManager pins
//     a pipe to every attached session, so it attaches right after the people
//     it follows and is the newest.
//
// With two or more people on the window, a control client in the latest slot
// makes tmux compute no size at all: the window stays frozen at the size it
// had, typically that of somebody who has left, and everyone still on it sees
// a small box of dots until one of them types or resizes. Agent Deck's control
// clients are the PipeManager's pipes and the insert-mode KeySender.
//
// HandLatestToViewer gives the slot back to a person. tmux has no command that
// sets the latest client: a client takes it by sending a resize, which its
// `tmux attach` process does on SIGWINCH (client.c) without changing the
// terminal's size.

// latestCandidateFormat is the list-clients format parseLatestCandidates reads.
const latestCandidateFormat = "#{client_pid}\t#{client_flags}\t#{client_activity}\t#{client_width}\t#{client_height}\t#{window_id}\t#{status}"

// latestCandidate is a person's client on a window: one that may hold the
// window's latest slot. cols x rows is the window size it asks for (its
// terminal minus its status lines).
type latestCandidate struct {
	pid        int
	activity   int64
	cols, rows int
}

// HandLatestToViewer makes sure the window currently shown by target (a
// session, or a session:window) follows a person, never a control client.
// It does nothing unless the window's policy is `latest` and two or more
// people are on it (with one, tmux sizes the window to that person whoever
// holds the slot). Otherwise it signals the person whose terminal the window
// already fits, so a control client's attach changes nothing on screen, or,
// when it fits nobody (it is frozen at a departed client's size), the most
// recently active person: tmux's own rule for a departed latest client, minus
// the control clients. Best effort: every failure leaves tmux as it was.
func HandLatestToViewer(socketName, target string) {
	out, err := runBoundedOutput(socketName, "display-message", "-p", "-t", target,
		"#{window_id}\t#{window_width}\t#{window_height}\t#{window-size}")
	if err != nil {
		return
	}
	fields := strings.Split(strings.TrimRight(string(out), "\r\n"), "\t")
	if len(fields) != 4 || fields[3] != windowSizePolicy {
		return
	}
	cols, err1 := strconv.Atoi(fields[1])
	rows, err2 := strconv.Atoi(fields[2])
	if err1 != nil || err2 != nil {
		return
	}
	out, err = runBoundedOutput(socketName, "list-clients", "-F", latestCandidateFormat)
	if err != nil {
		return
	}
	viewer, ok := pickLatestViewer(parseLatestCandidates(string(out), fields[0]), cols, rows)
	if !ok {
		return
	}
	if err := syscall.Kill(viewer.pid, syscall.SIGWINCH); err != nil {
		statusLog.Debug("hand_latest_to_viewer_failed",
			slog.String("target", target), slog.Int("pid", viewer.pid), slog.String("error", err.Error()))
	}
}

// parseLatestCandidates decodes latestCandidateFormat output into the people
// on windowID. Control-mode and ignore-size clients are skipped: tmux never
// sizes a window from them, so the slot must never go to them.
func parseLatestCandidates(out, windowID string) []latestCandidate {
	var candidates []latestCandidate
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) != 7 || f[5] != windowID {
			continue
		}
		flags := "," + f[1] + ","
		if strings.Contains(flags, ",control-mode,") || strings.Contains(flags, ",ignore-size,") {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil || pid <= 1 { // never signal init or a process group
			continue
		}
		cols, err1 := strconv.Atoi(f[3])
		height, err2 := strconv.Atoi(f[4])
		if err1 != nil || err2 != nil || cols < 1 {
			continue
		}
		rows, err := detachedPreviewPaneRows(height, f[6])
		if err != nil || rows < 1 {
			continue
		}
		activity, _ := strconv.ParseInt(f[2], 10, 64)
		candidates = append(candidates, latestCandidate{pid: pid, activity: activity, cols: cols, rows: rows})
	}
	return candidates
}

// pickLatestViewer chooses the person to hold the latest slot of a cols x rows
// window: among the people it fits, else among everyone, the most recently
// active. client_activity has one-second resolution, so a tie goes to the one
// listed last: tmux lists clients in the order they attached. ok is false with
// fewer than two people.
func pickLatestViewer(candidates []latestCandidate, cols, rows int) (best latestCandidate, ok bool) {
	if len(candidates) < 2 {
		return latestCandidate{}, false
	}
	bestFits := false
	for i, c := range candidates {
		fits := c.cols == cols && c.rows == rows
		if i == 0 || (fits && !bestFits) || (fits == bestFits && c.activity >= best.activity) {
			best, bestFits = c, fits
		}
	}
	return best, true
}
