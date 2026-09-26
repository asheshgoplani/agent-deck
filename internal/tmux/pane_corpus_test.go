package tmux

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The golden pane corpus (testdata/status_corpus) holds real tmux frames
// captured from live agent-deck sessions across harnesses and states on
// 2026-09-23 (local Mac plus three remote hosts), scrubbed of prose and
// labelled by eye with the frame-only verdict GetStatus should reach. Every
// detector change is scored against it: a frame that flips is a regression
// unless its label is what changed.
//
// labels.tsv columns: file, tool, expected (active|waiting|error), note.
// A note starting with "known blind spot" documents a miss that is accepted
// (the frame carries no cue any detector could read); those are reported but
// do not fail the test.
type corpusCase struct {
	file, tool, expected, note string
}

func loadStatusCorpus(t *testing.T) []corpusCase {
	t.Helper()
	dir := filepath.Join("testdata", "status_corpus")
	f, err := os.Open(filepath.Join(dir, "labels.tsv"))
	if err != nil {
		t.Fatalf("open labels: %v", err)
	}
	defer f.Close()
	var cases []corpusCase
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 3 {
			t.Fatalf("labels.tsv: malformed line %q", line)
		}
		c := corpusCase{file: parts[0], tool: parts[1], expected: parts[2]}
		if len(parts) == 4 {
			c.note = parts[3]
		}
		if _, err := os.Stat(filepath.Join(dir, c.file+".txt")); err != nil {
			t.Fatalf("labels.tsv names %s but the frame is missing: %v", c.file, err)
		}
		cases = append(cases, c)
	}
	if len(cases) < 30 {
		t.Fatalf("corpus too small: %d frames", len(cases))
	}
	return cases
}

type corpusScore struct{ n, exact, tp, fp, fn int }

func (s corpusScore) precision() float64 {
	if s.tp+s.fp == 0 {
		return 1
	}
	return float64(s.tp) / float64(s.tp+s.fp)
}

func (s corpusScore) recall() float64 {
	if s.tp+s.fn == 0 {
		return 1
	}
	return float64(s.tp) / float64(s.tp+s.fn)
}

// TestStatusCorpus_FrameVerdicts scores ClassifyPaneFrame against every
// labelled frame and prints per-harness "running" precision/recall. It fails
// on any mismatch that is not a documented blind spot.
func TestStatusCorpus_FrameVerdicts(t *testing.T) {
	dir := filepath.Join("testdata", "status_corpus")
	scores := map[string]*corpusScore{}
	var failures, blindSpots []string
	for _, c := range loadStatusCorpus(t) {
		raw, err := os.ReadFile(filepath.Join(dir, c.file+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		got := string(ClassifyPaneFrame(c.tool, string(raw)))
		sc := scores[c.tool]
		if sc == nil {
			sc = &corpusScore{}
			scores[c.tool] = sc
		}
		sc.n++
		switch {
		case got == "active" && c.expected == "active":
			sc.tp++
		case got == "active":
			sc.fp++
		case c.expected == "active":
			sc.fn++
		}
		if got == c.expected {
			sc.exact++
			continue
		}
		line := fmt.Sprintf("%s (%s): want %s, got %s  [%s]", c.file, c.tool, c.expected, got, c.note)
		if strings.HasPrefix(strings.ToLower(c.note), "known blind spot") {
			blindSpots = append(blindSpots, line)
			continue
		}
		failures = append(failures, line)
	}
	tools := make([]string, 0, len(scores))
	for tool := range scores {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	for _, tool := range tools {
		s := scores[tool]
		t.Logf("%-7s frames=%2d exact=%2d running-precision=%.2f (fp=%d) running-recall=%.2f (fn=%d)",
			tool, s.n, s.exact, s.precision(), s.fp, s.recall(), s.fn)
	}
	for _, b := range blindSpots {
		t.Logf("known blind spot: %s", b)
	}
	for _, f := range failures {
		t.Errorf("corpus mismatch: %s", f)
	}
}

// TestStatusCorpus_NoFalseRunning is the audit's headline invariant on its
// own: no labelled waiting/error frame may classify as active, for any tool.
func TestStatusCorpus_NoFalseRunning(t *testing.T) {
	dir := filepath.Join("testdata", "status_corpus")
	for _, c := range loadStatusCorpus(t) {
		if c.expected == "active" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, c.file+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if got := ClassifyPaneFrame(c.tool, string(raw)); got == FrameActive {
			t.Errorf("false running: %s (%s) labelled %s classifies active [%s]", c.file, c.tool, c.expected, c.note)
		}
	}
}

// TestTrimClaudeTrailingRoster pins the frame normalisation on its own.
func TestTrimClaudeTrailingRoster(t *testing.T) {
	frame := "✳ Pondering… (5m 11s · ↓ 18.7k tokens)\n───\n❯ \n───\n  [p] u@h:/x | ctx:1%\n  ⏵⏵ bypass permissions on · ← for agents\n  ⏺ main\n  ◯ general-purpose  Task 1\n  ◯ Explore  Task 2\n\n"
	got := trimClaudeTrailingRoster(frame)
	if strings.Contains(got, "general-purpose") || !strings.HasSuffix(got, "← for agents") {
		t.Fatalf("roster not trimmed:\n%s", got)
	}
	keep := "❯ \n  ⏵⏵ bypass permissions on\n Enter to confirm · Esc to cancel"
	if trimClaudeTrailingRoster(keep) != keep {
		t.Fatal("non-roster text after the footer must keep the frame intact")
	}
	if trimClaudeTrailingRoster("plain\n❯ ") != "plain\n❯ " {
		t.Fatal("frame without footer must be untouched")
	}
}
