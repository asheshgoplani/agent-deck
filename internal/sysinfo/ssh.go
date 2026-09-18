package sysinfo

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// SSHSession is one user's live remote logins on this host, aggregated:
// how many, since when (the oldest), and where the oldest came from.
type SSHSession struct {
	User     string
	Count    int
	HasSince bool
	Since    time.Time
	From     string
}

// SSHSessions is the "who is connected over SSH" snapshot. Available is
// false when it could not be gathered (no `who`, an output shape this
// parser does not know); Error then says why, so the caller reports unknown
// rather than "nobody".
type SSHSessions struct {
	Available bool
	Error     string
	Sessions  []SSHSession
}

// whoLine matches one line of `who` on GNU coreutils (Linux: "user pts/1
// 2026-09-18 09:10 (host)") and BSD/macOS ("user ttys001 Sep 18 09:10
// (host)"). The host is optional; a local login (console, tty1) has none.
var whoLine = regexp.MustCompile(`^(\S+)\s+(\S+)\s+((?:\d{4}-\d{2}-\d{2} \d{2}:\d{2})|(?:[A-Z][a-z]{2}\s+\d{1,2} \d{2}:\d{2}))(?:\s+(.*?))?\s*$`)

var whoHost = regexp.MustCompile(`\((.*)\)`)

// whoIsRemote reports whether a `who` host column names a remote login:
// anything but empty (local tty), an X display (":0"), or a tmux/screen
// pseudo-host ("tmux(276714).%820"), which is a pane inside a multiplexer,
// not a connection to the machine. mosh ("mosh [4242]") is remote.
func whoIsRemote(host string) bool {
	if host == "" || strings.HasPrefix(host, ":") {
		return false
	}
	return !strings.HasPrefix(host, "tmux(") && !strings.HasPrefix(host, "screen")
}

// parseWhoTime parses the login time column in either shape. The BSD form
// has no year: it is now's year unless that lands in the future, which
// means the login was last year.
func parseWhoTime(s string, now time.Time, loc *time.Location) (time.Time, bool) {
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, loc); err == nil {
		return t, true
	}
	fields := strings.Fields(s)
	if len(fields) != 3 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("Jan 2 15:04 2006", fmt.Sprintf("%s %s %s %d", fields[0], fields[1], fields[2], now.Year()), loc)
	if err != nil {
		return time.Time{}, false
	}
	if t.After(now.Add(24 * time.Hour)) {
		t = t.AddDate(-1, 0, 0)
	}
	return t, true
}

// ParseWho turns `who` output into per-user remote sessions sorted by user.
// A line the parser does not recognise is an error: better "unknown" than a
// confident "nobody connected" from a `who` this code has never seen.
func ParseWho(output string, now time.Time, loc *time.Location) ([]SSHSession, error) {
	byUser := map[string]*SSHSession{}
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := whoLine.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("unrecognised who line: %q", line)
		}
		host := ""
		if h := whoHost.FindStringSubmatch(m[4]); h != nil {
			host = h[1]
		}
		if !whoIsRemote(host) {
			continue
		}
		since, hasSince := parseWhoTime(m[3], now, loc)
		s, ok := byUser[m[1]]
		if !ok {
			s = &SSHSession{User: m[1]}
			byUser[m[1]] = s
		}
		s.Count++
		if hasSince && (!s.HasSince || since.Before(s.Since)) {
			s.HasSince, s.Since, s.From = true, since, host
		} else if !s.HasSince && s.From == "" {
			s.From = host
		}
	}
	out := make([]SSHSession, 0, len(byUser))
	for _, s := range byUser {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out, nil
}

// whoCommand runs `who` (POSIX, utmp-backed, no root needed on Linux or
// macOS); a test seam.
var whoCommand = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "who").Output()
}

// CollectSSHSessions gathers who is connected over SSH right now, via
// `who`. Portable choice: both platforms ship it, both read the same utmp
// record type without privileges, and reading utmp/utmpx directly would
// need two binary layouts (glibc 384-byte utmp vs. Darwin utmpx) for the
// same answer.
func CollectSSHSessions() SSHSessions {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := whoCommand(ctx)
	if err != nil {
		// A non-zero exit that still printed lines is parsed like a clean
		// run; one that printed nothing reports its stderr instead.
		var exitErr *exec.ExitError
		switch {
		case !errors.As(err, &exitErr):
			return SSHSessions{Error: "who: " + err.Error()}
		case len(out) == 0:
			return SSHSessions{Error: "who: " + strings.TrimSpace(string(exitErr.Stderr))}
		}
	}
	sessions, parseErr := ParseWho(string(out), time.Now(), time.Local)
	if parseErr != nil {
		return SSHSessions{Error: parseErr.Error()}
	}
	return SSHSessions{Available: true, Sessions: sessions}
}

// SSHCollector refreshes CollectSSHSessions in the background at a fixed
// interval, so a render path (the header's "ssh" field) reads a cached
// snapshot and never spawns `who` on a frame.
type SSHCollector struct {
	mu       sync.RWMutex
	snapshot SSHSessions
	interval time.Duration
	stopCh   chan struct{}
	once     sync.Once
}

// NewSSHCollector returns a collector polling every interval (30s when
// zero or less). Call Start to begin.
func NewSSHCollector(interval time.Duration) *SSHCollector {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &SSHCollector{interval: interval, stopCh: make(chan struct{})}
}

// Start begins collection in a goroutine; it never blocks the caller.
func (c *SSHCollector) Start() {
	go func() {
		c.collect()
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.collect()
			case <-c.stopCh:
				return
			}
		}
	}()
}

// Stop ends collection.
func (c *SSHCollector) Stop() { c.once.Do(func() { close(c.stopCh) }) }

// Get returns the latest snapshot (Available false until the first poll).
func (c *SSHCollector) Get() SSHSessions {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot
}

func (c *SSHCollector) collect() {
	snapshot := CollectSSHSessions()
	c.mu.Lock()
	c.snapshot = snapshot
	c.mu.Unlock()
}
