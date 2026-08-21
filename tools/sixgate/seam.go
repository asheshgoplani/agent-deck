package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Seam divergence: the free evidence two drivers produce by disagreeing.
//
// Driver A boots the real model in process; Driver B runs the shipped binary in
// a real terminal. When both execute the SAME script against the SAME recorded
// case, every frame they disagree about is a fact nobody had to think to look
// for. That is the entire argument for keeping two drivers instead of one.
//
// Most disagreements are uninteresting chrome: Driver A reports version 0.0.0
// because it is a package build, labels its profile differently, and paints no
// host-stat widget. Reporting those as findings would bury the one that matters.
// So this report separates a NUMERIC DISAGREEMENT — two lines identical except
// for their digits — from everything else, and puts it first. A number that
// changes when nothing but the driver changed is either a bug or a dependency
// nobody documented, and both are worth knowing before a feature is called done.
//
// The comparison is alignment-free on purpose. Frames differ in line COUNT (the
// binary paints a tip row the package build does not), so a positional diff
// would misalign everything after the first insertion and drown the signal. Two
// lines are matched instead by their digit-stripped shape, which finds the pair
// that says the same thing with different figures wherever either of them sits
// on the screen.

// digitRun matches a number so a line can be compared by shape.
var digitRun = regexp.MustCompile(`\d+`)

// seamFinding is one line the two drivers rendered with different figures.
type seamFinding struct {
	Frame string
	Shape string
	A     string
	B     string
}

// seamFrameStat is one frame's comparison.
type seamFrameStat struct {
	name             string
	onlyA, onlyB     int
	linesA, linesB   int
	numericFindings  int
	comparedShapes   int
	identicalOverall bool
}

// seamComparison is the whole A-versus-B result for one pair of drive
// directories.
type seamComparison struct {
	numeric []seamFinding
	stats   []seamFrameStat
}

// compareSeamDirs is the single implementation of the comparison. G1 calls it
// through writeSeamReport for the case it drove; G3's matrix calls it for every
// declared seam pair. One implementation is the point: two would drift, and a
// seam report that disagrees with itself is worse than none.
func compareSeamDirs(dirA, dirB string) (seamComparison, error) {
	var out seamComparison
	for _, d := range []string{dirA, dirB} {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			return out, nil
		}
	}
	frames, err := filepath.Glob(filepath.Join(dirA, "*.screen.txt"))
	if err != nil {
		return out, err
	}
	sort.Strings(frames)

	for _, fa := range frames {
		name := filepath.Base(fa)
		fb := filepath.Join(dirB, name)
		rawB, err := os.ReadFile(fb) //nolint:gosec // path from the gate tree
		if err != nil {
			continue
		}
		rawA, err := os.ReadFile(fa) //nolint:gosec // path from the gate tree
		if err != nil {
			continue
		}
		a := shapeIndex(string(rawA))
		b := shapeIndex(string(rawB))
		st := seamFrameStat{name: name, linesA: countLines(string(rawA)), linesB: countLines(string(rawB))}
		st.identicalOverall = string(rawA) == string(rawB)

		for shape, la := range a {
			lb, ok := b[shape]
			if !ok {
				st.onlyA++
				continue
			}
			st.comparedShapes++
			if la != lb {
				st.numericFindings++
				out.numeric = append(out.numeric, seamFinding{Frame: name, Shape: shape, A: la, B: lb})
			}
		}
		for shape := range b {
			if _, ok := a[shape]; !ok {
				st.onlyB++
			}
		}
		out.stats = append(out.stats, st)
	}
	return out, nil
}

// writeSeamReport compares a Driver A drive with the Driver B drive of the same
// recorded case and writes the result next to them. It returns the path written,
// or "" when there is nothing to compare.
func writeSeamReport(g1Root, caseName string) (string, error) {
	dirA := filepath.Join(g1Root, caseName)
	dirB := filepath.Join(g1Root, "pane-"+caseName)
	cmp, err := compareSeamDirs(dirA, dirB)
	if err != nil {
		return "", err
	}
	numeric, stats := cmp.numeric, cmp.stats
	if len(stats) == 0 {
		return "", nil
	}

	var s strings.Builder
	fmt.Fprintf(&s, "# Seam divergence — `%s`\n\n", caseName)
	s.WriteString("Driver A (in-process model, `" + caseName + "/`) versus Driver B (shipped binary in a\n")
	s.WriteString("real terminal, `pane-" + caseName + "/`), same script, same recorded case.\n\n")
	s.WriteString("Two drivers that agree prove little on their own. Two that disagree hand over a\n")
	s.WriteString("fact for free. This report exists to make the second kind findable, and to keep\n")
	s.WriteString("chrome differences from burying it.\n\n")

	s.WriteString("## Numeric disagreements — the ones that matter\n\n")
	s.WriteString("Lines that say the same thing with different figures. A number that moves when\n")
	s.WriteString("nothing but the driver changed is either a bug or an undocumented dependency.\n\n")
	if len(numeric) == 0 {
		s.WriteString("None. Every line the two drivers both rendered carried identical figures.\n\n")
	} else {
		for _, f := range numeric {
			fmt.Fprintf(&s, "**`%s`**\n\n```\nA: %s\nB: %s\n```\n\n", f.Frame, f.A, f.B)
		}
	}

	s.WriteString("## Per-frame summary\n\n")
	s.WriteString("`only A` / `only B` count lines whose shape appears on one side alone: chrome the\n")
	s.WriteString("two drivers legitimately paint differently (the package build reports version\n")
	s.WriteString("`0.0.0`, labels its profile differently, and paints no host-stat widget).\n\n")
	s.WriteString("| frame | lines A/B | shapes compared | numeric disagreements | only A | only B |\n")
	s.WriteString("|-------|-----------|-----------------|-----------------------|--------|--------|\n")
	for _, st := range stats {
		fmt.Fprintf(&s, "| `%s` | %d/%d | %d | %d | %d | %d |\n",
			st.name, st.linesA, st.linesB, st.comparedShapes, st.numericFindings, st.onlyA, st.onlyB)
	}
	s.WriteString("\n")

	out := filepath.Join(g1Root, "seam-"+caseName+".md")
	if err := os.WriteFile(out, []byte(s.String()), 0o644); err != nil { //nolint:gosec // committed artifact
		return "", err
	}
	return out, nil
}

// shapeIndex maps each non-blank line's digit-stripped shape to the line
// itself. A shape seen more than once is dropped: an ambiguous match would
// invent a disagreement out of two unrelated rows that happen to look alike.
func shapeIndex(content string) map[string]string {
	seen := map[string]int{}
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimRight(line, " \t")
		if strings.TrimSpace(t) == "" {
			continue
		}
		shape := digitRun.ReplaceAllString(t, "#")
		seen[shape]++
		out[shape] = t
	}
	for shape, n := range seen {
		if n > 1 {
			delete(out, shape)
		}
	}
	return out
}

func countLines(content string) int {
	return len(strings.Split(strings.TrimRight(content, "\n"), "\n"))
}
