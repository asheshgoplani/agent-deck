package tmux

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Viewer is one interactive tmux client attached to a session: a person's
// terminal, never agent-deck's own control-mode pipes (those are filtered
// out; they have no size and nobody is looking at them).
type Viewer struct {
	// Name is tmux's client name, which is the client's tty path.
	Name string `json:"name"`
	TTY  string `json:"tty"`
	// Width and Height are the client's terminal size in cells.
	Width  int `json:"width"`
	Height int `json:"height"`
	// Activity is the client's last input or attach time.
	Activity time.Time `json:"activity"`
	// User is the account the client runs as (tmux >= 3.2 reports it; older
	// servers fall back to the owner of the tty). Empty when unknown.
	User string `json:"user,omitempty"`
}

// viewerFormat is the list-clients format the viewer listings parse. Tab
// separated so an empty field (client_user on tmux < 3.2) keeps its column;
// the session name comes first so one server-wide listing can be grouped.
const viewerFormat = "#{session_name}\t#{client_name}\t#{client_tty}\t#{client_width}x#{client_height}\t#{client_activity}\t#{client_user}\t#{client_control_mode}"

// ListViewers returns the people attached to sessionName on socketName, most
// recently active first. An error means tmux could not be asked (server
// gone, timeout); an empty list means nobody is attached.
func ListViewers(ctx context.Context, socketName, sessionName string) ([]Viewer, error) {
	out, err := commandOutput(tmuxExecContext(ctx, socketName, "list-clients", "-t", sessionName, "-F", viewerFormat))
	if err != nil {
		return nil, err
	}
	return parseViewers(string(out), ttyOwner)[sessionName], nil
}

// ListAllViewers returns the viewers of every session on socketName, keyed
// by session name; sessions nobody is attached to have no key. One
// subprocess for the whole server, which is what listings and the TUI's
// row badges want. A socket with no server is an error (unknown): every
// session on it is stopped, and a stopped session has no viewers to report.
func ListAllViewers(ctx context.Context, socketName string) (map[string][]Viewer, error) {
	out, err := commandOutput(tmuxExecContext(ctx, socketName, "list-clients", "-F", viewerFormat))
	if err != nil {
		return nil, err
	}
	return parseViewers(string(out), ttyOwner), nil
}

// parseViewers decodes list-clients output into viewers by session name,
// each list most recently active first. owner resolves a tty to its owning
// user when tmux did not report one.
func parseViewers(out string, owner func(tty string) string) map[string][]Viewer {
	bySession := map[string][]Viewer{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) < 5 || fields[1] == "" {
			continue
		}
		if len(fields) > 6 && fields[6] == "1" {
			continue // control-mode client: a program, not a viewer
		}
		width, height, _ := parseSize(fields[3])
		v := Viewer{Name: fields[1], TTY: fields[2], Width: width, Height: height}
		if epoch, err := strconv.ParseInt(fields[4], 10, 64); err == nil && epoch > 0 {
			v.Activity = time.Unix(epoch, 0)
		}
		if len(fields) > 5 {
			v.User = fields[5]
		}
		if v.User == "" && owner != nil {
			v.User = owner(v.TTY)
		}
		bySession[fields[0]] = append(bySession[fields[0]], v)
	}
	for _, viewers := range bySession {
		sort.SliceStable(viewers, func(i, j int) bool { return viewers[i].Activity.After(viewers[j].Activity) })
	}
	return bySession
}

// parseSize decodes "WxH".
func parseSize(s string) (width, height int, ok bool) {
	w, h, found := strings.Cut(strings.TrimSpace(s), "x")
	if !found {
		return 0, 0, false
	}
	width, err1 := strconv.Atoi(w)
	height, err2 := strconv.Atoi(h)
	return width, height, err1 == nil && err2 == nil
}

// ttyOwner names the user owning the tty device, for tmux servers older
// than 3.2 (no client_user format).
func ttyOwner(tty string) string {
	if tty == "" {
		return ""
	}
	info, err := os.Stat(tty)
	if err != nil {
		return ""
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10))
	if err != nil {
		return ""
	}
	return u.Username
}

// Label is the viewer's one-line form: "ashesh (200x60, active 5s ago)".
// A viewer whose user is unknown is named by its tty.
func (v Viewer) Label(now time.Time) string {
	return fmt.Sprintf("%s (%dx%d, %s)", v.DisplayName(), v.Width, v.Height, v.ActivityLabel(now))
}

// DisplayName is the user when known, else the tty (never empty).
func (v Viewer) DisplayName() string {
	if v.User != "" {
		return v.User
	}
	if v.TTY != "" {
		return strings.TrimPrefix(v.TTY, "/dev/")
	}
	return v.Name
}

// ActivityLabel is "active 5s ago", "3m ago", "2h ago" or "idle" when the
// activity time is unknown.
func (v Viewer) ActivityLabel(now time.Time) string {
	if v.Activity.IsZero() {
		return "idle"
	}
	d := max(now.Sub(v.Activity), 0)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("active %ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// FormatViewers joins viewer labels with " · " (empty for no viewers).
func FormatViewers(viewers []Viewer) string {
	return FormatViewersAt(viewers, time.Now())
}

// FormatViewersAt is FormatViewers with the activity ages taken from now.
func FormatViewersAt(viewers []Viewer, now time.Time) string {
	labels := make([]string, 0, len(viewers))
	for _, v := range viewers {
		labels = append(labels, v.Label(now))
	}
	return strings.Join(labels, " · ")
}

// viewerNoticeDelay is how long the "also viewing" notice stays on the tmux
// status line of the attaching client (display-message -d, tmux >= 3.2).
const viewerNoticeDelay = 5 * time.Second

// announceOtherViewers tells the client that just attached who else is
// looking at the session, as a tmux status-line message on that client
// only. others is the viewer list captured before the attach, so it is
// exactly the set the new client is joining; the new client is found as the
// one tty not in that set (polled, since tmux registers the client a moment
// after the subprocess starts). Nothing is shown when nobody else is there.
func (s *Session) announceOtherViewers(ctx context.Context, others []Viewer, window time.Duration) {
	if len(others) == 0 {
		return
	}
	known := make(map[string]bool, len(others))
	for _, v := range others {
		known[v.Name] = true
	}
	deadline := time.Now().Add(window)
	for {
		viewers, err := ListViewers(ctx, s.SocketName, s.Name)
		if err == nil {
			if newcomer, ok := firstUnknownViewer(viewers, known); ok {
				s.showViewerNotice(ctx, newcomer, "also viewing: "+FormatViewers(others))
				return
			}
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(configErrorViewPoll):
		}
	}
}

// firstUnknownViewer picks the first viewer whose name is not in known.
func firstUnknownViewer(viewers []Viewer, known map[string]bool) (Viewer, bool) {
	for _, v := range viewers {
		if !known[v.Name] {
			return v, true
		}
	}
	return Viewer{}, false
}

// showViewerNotice puts msg on the status line of client v for
// viewerNoticeDelay.
func (s *Session) showViewerNotice(ctx context.Context, v Viewer, msg string) {
	delay := strconv.Itoa(int(viewerNoticeDelay / time.Millisecond))
	if err := s.tmuxCmdContext(ctx, "display-message", "-c", v.Name, "-d", delay, msg).Run(); err != nil {
		// tmux < 3.2 has no -d; show it for the default display-time.
		_ = s.tmuxCmdContext(ctx, "display-message", "-c", v.Name, msg).Run()
	}
	statusLog.Debug("shared_view_notice", slog.String("session", s.Name),
		slog.String("client", v.Name), slog.String("message", msg))
}
