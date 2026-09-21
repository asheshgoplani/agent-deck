package update

import (
	"context"
	"regexp"
	"strings"
	"sync"
)

// UnattendedProgress is the remote-phase progress of a running unattended
// updater child, read from its stdout so the TUI can say "2/4 remotes"
// instead of a bare "still running". Safe for one writer (the child's
// output copier) and any number of readers.
type UnattendedProgress struct {
	mu    sync.Mutex
	total int
	done  int
	line  []byte
}

type progressCtxKey struct{}

// WithProgress makes RunUnattendedInstall report the child's remote
// progress into p.
func WithProgress(ctx context.Context, p *UnattendedProgress) context.Context {
	return context.WithValue(ctx, progressCtxKey{}, p)
}

// remoteHeaderRe matches the two header lines that open a remote phase:
// "nudging 4 remote(s) to check ..." and "sweep_remotes is on: pushing
// v1.2.3 to 4 remote(s)".
var remoteHeaderRe = regexp.MustCompile(`^(?:nudging|sweep_remotes is on: pushing \S+ to) (\d+) remote`)

// Remotes returns how many remotes the phase covers and how many have
// reported; total is 0 until the phase has started.
func (p *UnattendedProgress) Remotes() (done, total int) {
	if p == nil {
		return 0, 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done, p.total
}

// Write implements io.Writer over the child's output.
func (p *UnattendedProgress) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range b {
		if c != '\n' {
			p.line = append(p.line, c)
			continue
		}
		p.consume(string(p.line))
		p.line = p.line[:0]
	}
	return len(b), nil
}

// consume handles one complete line: a header starts a phase, an indented
// line after it is one remote's report.
func (p *UnattendedProgress) consume(line string) {
	if m := remoteHeaderRe.FindStringSubmatch(line); m != nil {
		n := 0
		for _, d := range m[1] {
			n = n*10 + int(d-'0')
		}
		p.total, p.done = n, 0
		return
	}
	if p.total > 0 && p.done < p.total && strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "" {
		p.done++
	}
}
