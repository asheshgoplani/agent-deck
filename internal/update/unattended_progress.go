package update

import (
	"context"
	"regexp"
	"strconv"
	"sync"
)

// UnattendedProgress is what the TUI knows about the remote phase of a
// running unattended updater child, read from its stdout: which model it is
// in (the default pull-only nudge, or the opt-in byte-pushing sweep) and how
// many remotes it covers. It deliberately keeps no per-remote counter: the
// nudge prints its N result lines only after all of them are in, and the
// sweep prints per-step lines, so a "2/4" would be a lie. Safe for one
// writer (the child's output copier) and any number of readers.
type UnattendedProgress struct {
	mu    sync.Mutex
	mode  RemotePhase
	total int
	line  []byte
}

// RemotePhase is the remote step an unattended run has reached.
type RemotePhase int

const (
	// PhaseNone: no remote phase has started (installing, re-registering
	// launch agents, or a run with no remotes).
	PhaseNone RemotePhase = iota
	// PhaseNudge: nudging every remote to pull (the default).
	PhaseNudge
	// PhaseSweep: pushing the binary to every remote ([updates].sweep_remotes).
	PhaseSweep
)

// maxProgressLine bounds the buffered partial line; a child that prints
// megabytes without a newline must not grow the TUI's memory.
const maxProgressLine = 4 * 1024

type progressCtxKey struct{}

// WithProgress makes RunUnattendedInstall report the child's remote
// progress into p.
func WithProgress(ctx context.Context, p *UnattendedProgress) context.Context {
	return context.WithValue(ctx, progressCtxKey{}, p)
}

// The header lines that open a remote phase, as update_cli.go prints them:
// "nudging 4 remote(s) to check for v1.2.3 now" and "sweep_remotes is on:
// pushing v1.2.3 to 4 remote(s)".
var (
	nudgeHeaderRe = regexp.MustCompile(`^nudging (\d+) remote`)
	sweepHeaderRe = regexp.MustCompile(`^sweep_remotes is on: pushing \S+ to (\d+) remote`)
)

// Phase returns the current remote phase and how many remotes it covers.
func (p *UnattendedProgress) Phase() (RemotePhase, int) {
	if p == nil {
		return PhaseNone, 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mode, p.total
}

// Write implements io.Writer over the child's output.
func (p *UnattendedProgress) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range b {
		if c == '\n' {
			p.consume(string(p.line))
			p.line = p.line[:0]
		} else if len(p.line) < maxProgressLine {
			p.line = append(p.line, c)
		}
	}
	return len(b), nil
}

func (p *UnattendedProgress) consume(line string) {
	for _, h := range []struct {
		re   *regexp.Regexp
		mode RemotePhase
	}{{nudgeHeaderRe, PhaseNudge}, {sweepHeaderRe, PhaseSweep}} {
		if m := h.re.FindStringSubmatch(line); m != nil {
			n, err := strconv.Atoi(m[1])
			if err == nil {
				p.mode, p.total = h.mode, n
			}
			return
		}
	}
}

// BufferedLen is the size of the pending partial line (tests pin its cap).
func (p *UnattendedProgress) BufferedLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.line)
}
