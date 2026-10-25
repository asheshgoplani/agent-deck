package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Operation is one catalogue entry (bench/ops/catalogue.json).
type Operation struct {
	Op               string             `json:"op"`
	Name             string             `json:"name"`
	TargetMS         *float64           `json:"target_ms"`
	TargetBySessions map[string]float64 `json:"target_by_sessions,omitempty"`
	Means            string             `json:"means"`
	How              string             `json:"how"`
	Bench            string             `json:"bench"`
	Live             string             `json:"live"`
	Area             string             `json:"area"`
	Status           string             `json:"status"`
	Exec             bool               `json:"exec,omitempty"`
	Note             string             `json:"note"`
}

// Catalogue is bench/ops/catalogue.json.
type Catalogue struct {
	Operations []Operation `json:"operations"`
}

// target returns the target for a fixture size, if any.
func (o Operation) target(sessions int) *float64 {
	if v, ok := o.TargetBySessions[strconv.Itoa(sessions)]; ok {
		return &v
	}
	return o.TargetMS
}

func (c *Catalogue) byBench(name string) (Operation, bool) {
	for _, o := range c.Operations {
		if o.Bench != "" && o.Bench == name {
			return o, true
		}
	}
	return Operation{}, false
}

func (c *Catalogue) index(op string) int {
	for i, o := range c.Operations {
		if o.Op == op {
			return i
		}
	}
	return len(c.Operations)
}

// Result is one benchmark (one size) in a results file.
type Result struct {
	ID          string   `json:"id"`
	Bench       string   `json:"bench"`
	Op          string   `json:"op"`
	Sessions    int      `json:"sessions"`
	Runs        int      `json:"runs"`
	MedianMS    float64  `json:"median_ms"`
	MinMS       float64  `json:"min_ms"`
	MaxMS       float64  `json:"max_ms"`
	AllocsPerOp *float64 `json:"allocs_per_op"`
	TargetMS    *float64 `json:"target_ms"`
}

// Results is bench/ops/results/<label>.json, the contract shared with the
// owner dashboard and the docs.
type Results struct {
	Label      string   `json:"label"`
	Ref        string   `json:"ref"`
	SHA        string   `json:"sha"`
	Date       string   `json:"date"`
	CommitDate string   `json:"commit_date,omitempty"`
	Host       string   `json:"host"`
	Results    []Result `json:"results"`
	Skipped    []string `json:"skipped,omitempty"`
}

// order is the column order: commit date when known, else run date, then label.
func (r Results) order() string {
	if r.CommitDate != "" {
		return r.CommitDate
	}
	return r.Date
}

var (
	benchLine = regexp.MustCompile(`^(BenchmarkOp_\S+?)(?:-\d+)?\s+(\d+)\s+([0-9.]+) ns/op(.*)$`)
	allocsRe  = regexp.MustCompile(`([0-9.]+) allocs/op`)
	sizeRe    = regexp.MustCompile(`(?:^|/)sessions=(\d+)`)
	skipLine  = regexp.MustCompile(`^\s*--- (SKIP|FAIL): (BenchmarkOp_\S+)`)
)

// Parse reads go test -bench output (any number of -count runs and packages)
// and returns one Result per benchmark name with the median over runs.
func Parse(raw string, c *Catalogue) *Results {
	type acc struct {
		ns, allocs []float64
	}
	runs := map[string]*acc{}
	var order []string
	res := &Results{}
	seenSkip := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if m := skipLine.FindStringSubmatch(line); m != nil {
			key := m[2] + " (" + strings.ToLower(m[1]) + ")"
			if !seenSkip[key] {
				seenSkip[key] = true
				res.Skipped = append(res.Skipped, key)
			}
			continue
		}
		m := benchLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ns, err := strconv.ParseFloat(m[3], 64)
		if err != nil {
			continue
		}
		a := runs[m[1]]
		if a == nil {
			a = &acc{}
			runs[m[1]] = a
			order = append(order, m[1])
		}
		a.ns = append(a.ns, ns)
		if am := allocsRe.FindStringSubmatch(m[4]); am != nil {
			if v, err := strconv.ParseFloat(am[1], 64); err == nil {
				a.allocs = append(a.allocs, v)
			}
		}
	}
	for _, name := range order {
		a := runs[name]
		base, _, _ := strings.Cut(name, "/")
		sessions := 0
		if m := sizeRe.FindStringSubmatch(name); m != nil {
			sessions, _ = strconv.Atoi(m[1])
		}
		r := Result{Bench: name, Sessions: sessions, Runs: len(a.ns)}
		op, ok := c.byBench(base)
		if ok {
			r.Op = op.Op
			r.TargetMS = op.target(sessions)
		} else {
			r.Op = base
		}
		r.ID = r.Op
		if sessions > 0 {
			r.ID = fmt.Sprintf("%s@%d", r.Op, sessions)
		}
		sorted := append([]float64(nil), a.ns...)
		sort.Float64s(sorted)
		r.MedianMS = round(median(sorted) / 1e6)
		r.MinMS = round(sorted[0] / 1e6)
		r.MaxMS = round(sorted[len(sorted)-1] / 1e6)
		if len(a.allocs) > 0 && !op.Exec {
			al := append([]float64(nil), a.allocs...)
			sort.Float64s(al)
			v := median(al)
			r.AllocsPerOp = &v
		}
		res.Results = append(res.Results, r)
	}
	return res
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// round keeps 6 significant digits so tiny (ns-scale) values survive.
func round(ms float64) float64 {
	if ms == 0 {
		return 0
	}
	mag := math.Pow(10, 5-math.Floor(math.Log10(math.Abs(ms))))
	return math.Round(ms*mag) / mag
}

func loadCatalogue(path string) (*Catalogue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Catalogue
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func loadResults(dir string) ([]*Results, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*Results
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var r Results
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if r.Label == "" || len(r.Results) == 0 {
			continue
		}
		out = append(out, &r)
	}
	return out, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

const (
	benchBegin   = "<!-- BENCH:BEGIN -->"
	benchEnd     = "<!-- BENCH:END -->"
	backlogBegin = "<!-- BACKLOG:BEGIN -->"
	backlogEnd   = "<!-- BACKLOG:END -->"
)

// Render returns doc with the two marked blocks regenerated from runs.
// Everything outside the markers is kept byte for byte.
func Render(doc string, c *Catalogue, runs []*Results) (string, error) {
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].order() != runs[j].order() {
			return runs[i].order() < runs[j].order()
		}
		return runs[i].Label < runs[j].Label
	})
	out, err := replaceBlock(doc, benchBegin, benchEnd, renderTable(c, runs))
	if err != nil {
		return "", err
	}
	return replaceBlock(out, backlogBegin, backlogEnd, renderBacklog(c, runs))
}

func replaceBlock(doc, begin, end, body string) (string, error) {
	i := strings.Index(doc, begin)
	j := strings.Index(doc, end)
	if i < 0 || j < 0 || j < i {
		return "", fmt.Errorf("markers %s ... %s not found in order", begin, end)
	}
	return doc[:i+len(begin)] + "\n" + body + doc[j:], nil
}

type rowKey struct {
	op       string
	bench    string
	sessions int
}

func rowsOf(c *Catalogue, runs []*Results) []rowKey {
	seen := map[rowKey]bool{}
	var keys []rowKey
	for _, r := range runs {
		for _, x := range r.Results {
			k := rowKey{x.Op, strings.SplitN(x.Bench, "/", 2)[0], x.Sessions}
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.SliceStable(keys, func(i, j int) bool {
		a, b := c.index(keys[i].op), c.index(keys[j].op)
		if a != b {
			return a < b
		}
		if keys[i].op != keys[j].op {
			return keys[i].op < keys[j].op
		}
		return keys[i].sessions < keys[j].sessions
	})
	return keys
}

func find(r *Results, k rowKey) *Result {
	for i := range r.Results {
		x := &r.Results[i]
		if x.Op == k.op && x.Sessions == k.sessions {
			return x
		}
	}
	return nil
}

func opFor(c *Catalogue, op string) Operation {
	if i := c.index(op); i < len(c.Operations) {
		return c.Operations[i]
	}
	return Operation{Op: op, Name: op}
}

func renderTable(c *Catalogue, runs []*Results) string {
	var b strings.Builder
	if len(runs) == 0 {
		b.WriteString("No results yet. Run `bench/ops/run.sh <label> <ref>`.\n")
		return b.String()
	}
	newest := runs[len(runs)-1]
	fmt.Fprintf(&b, "Generated by `tools/benchops` from `bench/ops/results/*.json`. Median ms per operation over the -count runs; newest: **%s** (%s, %s).\n\n", newest.Label, short(newest.SHA), newest.Host)
	b.WriteString("| Operation | Sessions | Target |")
	for _, r := range runs {
		fmt.Fprintf(&b, " %s |", r.Label)
	}
	b.WriteString(" Δ newest | Verdict |\n|---|---:|---:|")
	for range runs {
		b.WriteString("---:|")
	}
	b.WriteString("---:|---|\n")
	for _, k := range rowsOf(c, runs) {
		o := opFor(c, k.op)
		sessions := "-"
		if k.sessions > 0 {
			sessions = strconv.Itoa(k.sessions)
		}
		tgt := o.target(k.sessions)
		fmt.Fprintf(&b, "| %s (`%s`) | %s | %s |", o.Name, k.op, sessions, fmtTarget(tgt))
		var vals []*Result
		for _, r := range runs {
			x := find(r, k)
			vals = append(vals, x)
			if x == nil {
				b.WriteString(" n/a |")
			} else {
				fmt.Fprintf(&b, " %s |", fmtMS(x.MedianMS))
			}
		}
		fmt.Fprintf(&b, " %s | %s |\n", delta(vals), verdict(vals[len(vals)-1], tgt))
	}
	if len(newest.Skipped) > 0 {
		fmt.Fprintf(&b, "\nSkipped or failed on %s: %s.\n", newest.Label, strings.Join(newest.Skipped, ", "))
	}
	return b.String()
}

// delta compares the newest value with the closest earlier column that has one.
func delta(vals []*Result) string {
	last := vals[len(vals)-1]
	if last == nil {
		return "n/a"
	}
	for i := len(vals) - 2; i >= 0; i-- {
		if vals[i] != nil && vals[i].MedianMS > 0 {
			return fmt.Sprintf("%+.0f%%", (last.MedianMS/vals[i].MedianMS-1)*100)
		}
	}
	return "n/a"
}

func verdict(x *Result, tgt *float64) string {
	switch {
	case x == nil:
		return "no data"
	case tgt == nil || *tgt <= 0:
		return "no target"
	case x.MedianMS <= *tgt:
		return "ok"
	default:
		return fmt.Sprintf("**over** (%.1fx)", x.MedianMS / *tgt)
	}
}

func renderBacklog(c *Catalogue, runs []*Results) string {
	if len(runs) == 0 {
		return "No results yet.\n"
	}
	newest := runs[len(runs)-1]
	type item struct {
		r     Result
		o     Operation
		ratio float64
	}
	var items []item
	for _, x := range newest.Results {
		o := opFor(c, x.Op)
		tgt := o.target(x.Sessions)
		if tgt == nil || *tgt <= 0 {
			continue
		}
		if r := x.MedianMS / *tgt; r > 1 {
			items = append(items, item{x, o, r})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ratio > items[j].ratio })
	// One line per operation: its worst session count.
	seen := map[string]bool{}
	kept := items[:0]
	for _, it := range items {
		if !seen[it.r.Op] {
			seen[it.r.Op] = true
			kept = append(kept, it)
		}
	}
	items = kept
	if len(items) > 5 {
		items = items[:5]
	}
	var b strings.Builder
	if len(items) == 0 {
		fmt.Fprintf(&b, "No operation is over its target on **%s** (generated).\n", newest.Label)
		return b.String()
	}
	fmt.Fprintf(&b, "Operations over target on **%s**, worst first by median / target, one line per operation (generated).\n\n", newest.Label)
	for i, it := range items {
		size := ""
		if it.r.Sessions > 0 {
			size = fmt.Sprintf(", %d sessions", it.r.Sessions)
		}
		fmt.Fprintf(&b, "%d. **%s** (`%s`%s): %s vs %s target, %.1fx.", i+1, it.o.Name, it.r.Op, size, withUnit(it.r.MedianMS), fmtTarget(it.o.target(it.r.Sessions)), it.ratio)
		if it.o.Note != "" {
			fmt.Fprintf(&b, " %s", it.o.Note)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func fmtTarget(t *float64) string {
	if t == nil {
		return "-"
	}
	if *t < 0.001 {
		return fmtMS(*t)
	}
	return strconv.FormatFloat(*t, 'f', -1, 64) + " ms"
}

// withUnit is fmtMS with its unit: "12.3 ms", or "1.2 ns" for tiny values.
func withUnit(ms float64) string {
	s := fmtMS(ms)
	if strings.HasSuffix(s, " ns") {
		return s
	}
	return s + " ms"
}

func fmtMS(ms float64) string {
	switch {
	case ms == 0:
		return "0"
	case ms < 0.001:
		return fmt.Sprintf("%.1f ns", ms*1e6)
	case ms < 1:
		return strconv.FormatFloat(ms, 'f', 3, 64)
	case ms < 10:
		return strconv.FormatFloat(ms, 'f', 2, 64)
	case ms < 100:
		return strconv.FormatFloat(ms, 'f', 1, 64)
	default:
		return strconv.FormatFloat(ms, 'f', 0, 64)
	}
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
