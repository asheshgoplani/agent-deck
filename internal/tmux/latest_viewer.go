package tmux

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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
//
// A control client that asked for a size (`refresh-client -C`, as iTerm2's
// `tmux -CC` integration does) is a person too: tmux sizes the window from it
// and it may hold the slot. Only size-less control clients are nobody.

// latestSettle is how long a client event is left to settle before the slot
// is judged: tmux announces an attach before the newcomer's first resize,
// which is what makes the newcomer the latest client, and a hand-back in
// between would take the window from them.
const latestSettle = 250 * time.Millisecond

// latestCandidateFormat is the list-clients format parseLatestCandidates reads.
const latestCandidateFormat = "#{client_pid}\t#{client_flags}\t#{client_activity}\t#{client_width}\t#{client_height}\t#{window_id}\t#{status}"

// latestCandidate is a client on a window that may hold the window's latest
// slot. cols x rows is the window size it asks for (its terminal minus its
// status lines). A control candidate is never signalled and only its width is
// known: tmux reports no height for a control client.
type latestCandidate struct {
	pid        int
	activity   int64
	cols, rows int
	control    bool
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
	// The pid is what the client reported and may have changed hands since
	// list-clients: signal only our own tmux client.
	if !isOwnTmuxClient(viewer.pid) {
		statusLog.Debug("hand_latest_to_viewer_skipped",
			slog.String("target", target), slog.Int("pid", viewer.pid))
		return
	}
	if err := syscall.Kill(viewer.pid, syscall.SIGWINCH); err != nil {
		statusLog.Debug("hand_latest_to_viewer_failed",
			slog.String("target", target), slog.Int("pid", viewer.pid), slog.String("error", err.Error()))
	}
}

// parseLatestCandidates decodes latestCandidateFormat output into the clients
// on windowID that may hold its latest slot. ignore-size and suspended clients
// are skipped: tmux never sizes a window from them. Control-mode clients are
// kept as control candidates with their width, which is the default 80 for a
// size-less one such as agent-deck's own pipe.
func parseLatestCandidates(out, windowID string) []latestCandidate {
	var candidates []latestCandidate
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) != 7 || f[5] != windowID {
			continue
		}
		flags := "," + f[1] + ","
		if strings.Contains(flags, ",ignore-size,") || strings.Contains(flags, ",suspended,") {
			continue
		}
		cols, err := strconv.Atoi(f[3])
		if err != nil || cols < 1 {
			continue
		}
		if strings.Contains(flags, ",control-mode,") {
			candidates = append(candidates, latestCandidate{cols: cols, control: true})
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil || pid <= 1 { // never signal init or a process group
			continue
		}
		height, err := strconv.Atoi(f[4])
		if err != nil {
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

// pickLatestViewer chooses the person on a pty to hold the latest slot of a
// cols x rows window: among the people it fits, else among everyone, the most
// recently active. client_activity has one-second resolution, so a tie goes to
// the one listed last: tmux lists clients in the order they attached. ok is
// false with fewer than two people on ptys, and when the window is as wide as
// a control client: that is a sized control client (iTerm2 -CC) holding the
// slot, a person nobody may take the window from. The cost is that a window
// frozen at exactly 80 columns, a size-less control client's width, is left
// alone.
func pickLatestViewer(candidates []latestCandidate, cols, rows int) (best latestCandidate, ok bool) {
	people := 0
	bestFits := false
	for _, c := range candidates {
		if c.control {
			if c.cols == cols {
				return latestCandidate{}, false
			}
			continue
		}
		fits := c.cols == cols && c.rows == rows
		if people == 0 || (fits && !bestFits) || (fits == bestFits && c.activity >= best.activity) {
			best, bestFits = c, fits
		}
		people++
	}
	if people < 2 {
		return latestCandidate{}, false
	}
	return best, true
}

// isOwnTmuxClient reports whether pid is, right now, a tmux client process
// running as this user: #{client_pid} is whatever the client reported, and a
// pid can change hands between list-clients and the signal. Linux reads /proc;
// elsewhere a bounded ps. Anything unreadable answers false.
func isOwnTmuxClient(pid int) bool {
	uid, comm, ok := procUIDAndComm(pid)
	return ok && uid == os.Getuid() && isReapableTmuxClientComm(comm)
}

func procUIDAndComm(pid int) (uid int, comm string, ok bool) {
	if status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		commRaw, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil {
			return 0, "", false
		}
		for _, line := range strings.Split(string(status), "\n") {
			if fields := strings.Fields(line); len(fields) > 1 && fields[0] == "Uid:" {
				uid, err := strconv.Atoi(fields[1])
				return uid, strings.TrimSpace(string(commRaw)), err == nil
			}
		}
		return 0, "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), processProbeTimeout)
	defer cancel()
	// #nosec G204 -- "ps" is a fixed binary; only arg is strconv.Itoa(int).
	out, err := exec.CommandContext(ctx, "ps", "-o", "uid=,comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, "", false
	}
	uidText, commText, found := strings.Cut(strings.TrimSpace(string(out)), " ")
	if !found {
		return 0, "", false
	}
	uid, err = strconv.Atoi(uidText)
	return uid, filepath.Base(strings.TrimSpace(commText)), err == nil
}
