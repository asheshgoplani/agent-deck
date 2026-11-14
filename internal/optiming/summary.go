package optiming

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

// Target is the speed an operation should reach, at p95.
type Target struct {
	Op    string
	P95   time.Duration
	Means string // plain-words definition of the span
}

// Targets is the catalogue of timed operations (docs/performance.md lists the
// same names). An op without an exact entry falls back to the longest
// matching prefix entry ending in ".".
var Targets = []Target{
	{"tui.first_frame", 500 * time.Millisecond, "TUI process start to the first frame with the session list"},
	{"tui.attach", 300 * time.Millisecond, "Enter on a session to the first byte of the live pane"},
	{"tui.detach", 150 * time.Millisecond, "detach key to the session list drawn again"},
	{"tui.preview", 150 * time.Millisecond, "preview pane fetch for the selected session (after the 150 ms debounce)"},
	{"tui.status_pass", 250 * time.Millisecond, "one status sweep over every session (health.StatusPassBudget)"},
	{"daemon.pass", 100 * time.Millisecond, "one notify-daemon pass: reload sessions, refresh tmux, emit transitions"},
	{"remote.list", 2 * time.Second, "list sessions of one ssh remote (health.RemotePollBudget)"},
	{"web.menu", 100 * time.Millisecond, "web UI session menu request served"},
	{"update.check", 2 * time.Second, "release check against GitHub"},
	{"update.install", 30 * time.Second, "download, verify and install a release"},
	{"cli.list", 300 * time.Millisecond, "agent-deck list"},
	{"cli.status", 300 * time.Millisecond, "agent-deck status"},
	{"cli.events.publish", 100 * time.Millisecond, "agent-deck events publish (one call)"},
	{"cli.session.send", 3 * time.Second, "agent-deck session send, including the wait for a ready agent"},
	{"cli.session.start", 2 * time.Second, "agent-deck session start (tmux session created, agent launching)"},
	{"cli.launch", 3 * time.Second, "agent-deck launch (add + start + optional message)"},
	{"cli.", 500 * time.Millisecond, "any other agent-deck subcommand, process start to exit"},
}

// TargetFor returns the catalogue entry for op, if any.
func TargetFor(op string) (Target, bool) {
	best := -1
	for i, t := range Targets {
		if t.Op == op {
			return t, true
		}
		if strings.HasSuffix(t.Op, ".") && strings.HasPrefix(op, t.Op) && (best < 0 || len(t.Op) > len(Targets[best].Op)) {
			best = i
		}
	}
	if best >= 0 {
		return Targets[best], true
	}
	return Target{}, false
}

// Summary is one operation's numbers over a window.
type Summary struct {
	Op     string  `json:"op"`
	Count  int     `json:"count"`
	Fails  int     `json:"fails"`
	P50MS  float64 `json:"p50_ms"`
	P95MS  float64 `json:"p95_ms"`
	MaxMS  float64 `json:"max_ms"`
	Target float64 `json:"target_p95_ms,omitempty"`
	Over   bool    `json:"over_target"`
}

// ReadFile reads every entry at or after since from path and its rotated
// predecessor (path + ".1"). Malformed lines are skipped. A missing file is
// not an error.
func ReadFile(path string, since time.Time) ([]Entry, error) {
	var out []Entry
	for _, p := range []string{path + ".1", path} {
		f, err := os.Open(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, err
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.Op == "" {
				continue
			}
			if !e.Time.Before(since) {
				out = append(out, e)
			}
		}
		err = sc.Err()
		_ = f.Close()
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// Summarize groups entries per op, sorted by how far p95 is over target
// (worst first), then by name.
func Summarize(entries []Entry) []Summary {
	byOp := map[string][]float64{}
	fails := map[string]int{}
	for _, e := range entries {
		byOp[e.Op] = append(byOp[e.Op], e.MS)
		if e.Fail {
			fails[e.Op]++
		}
	}
	out := make([]Summary, 0, len(byOp))
	for op, ms := range byOp {
		sort.Float64s(ms)
		s := Summary{Op: op, Count: len(ms), Fails: fails[op], P50MS: Percentile(ms, 50), P95MS: Percentile(ms, 95), MaxMS: ms[len(ms)-1]}
		if t, ok := TargetFor(op); ok {
			s.Target = float64(t.P95.Microseconds()) / 1000
			s.Over = s.P95MS > s.Target
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := ratio(out[i]), ratio(out[j])
		if ri != rj {
			return ri > rj
		}
		return out[i].Op < out[j].Op
	})
	return out
}

func ratio(s Summary) float64 {
	if s.Target <= 0 {
		return 0
	}
	return s.P95MS / s.Target
}

// Percentile returns the nearest-rank percentile p of sorted values.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
