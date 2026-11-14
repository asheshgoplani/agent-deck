package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleCatalogue = `{"operations":[
 {"op":"tui.list_render","name":"TUI list render","target_ms":16,"target_by_sessions":{"500":33},"bench":"BenchmarkOp_TUIListRender","status":"measured"},
 {"op":"cli.list","name":"CLI list","target_ms":300,"bench":"BenchmarkOp_CLIList","exec":true,"status":"measured","note":"tmux probes per session."},
 {"op":"cli.events.publish","name":"Events publish","target_ms":100,"bench":"BenchmarkOp_CLIEventsPublish","exec":true,"status":"measured","note":"Flush waits for the 1 s sync tick."},
 {"op":"optiming.hook_off","name":"Hook off","target_ms":0.00001,"bench":"BenchmarkOp_TimingHookOff","status":"measured"},
 {"op":"tui.attach","name":"Attach","target_ms":300,"status":"live-only"}
]}`

const sampleRaw = `goos: linux
pkg: github.com/asheshgoplani/agent-deck/cmd/agent-deck
BenchmarkOp_CLIList/sessions=100-6         	      10	  90000000 ns/op	  136264 B/op	      65 allocs/op
BenchmarkOp_CLIList/sessions=100-6         	      10	 110000000 ns/op	  136264 B/op	      65 allocs/op
BenchmarkOp_CLIList/sessions=100-6         	      10	 100000000 ns/op	  136264 B/op	      65 allocs/op
BenchmarkOp_CLIEventsPublish-6             	       1	1017049252 ns/op	    6744 B/op	      56 allocs/op
--- SKIP: BenchmarkOp_CLISessionStart
    opbench_cli_test.go:1: tmux not installed
BenchmarkColdStart_Help-6                  	       1	   9050533 ns/op
PASS
pkg: github.com/asheshgoplani/agent-deck/internal/ui
BenchmarkOp_TUIListRender/sessions=500-6   	     800	   1500000 ns/op	  193336 B/op	    2749 allocs/op
BenchmarkOp_TUIListRender/sessions=500-6   	     800	   1700000 ns/op	  193336 B/op	    2751 allocs/op
BenchmarkOp_TimingHookOff                  	1000000000	         0.5000 ns/op	       0 B/op	       0 allocs/op
BenchmarkOp_Unknown/sessions=10-6          	     100	   2000000 ns/op
ok  	github.com/asheshgoplani/agent-deck/internal/ui	3.1s
`

func cat(t *testing.T) *Catalogue {
	t.Helper()
	var c Catalogue
	if err := json.Unmarshal([]byte(sampleCatalogue), &c); err != nil {
		t.Fatal(err)
	}
	return &c
}

func byID(r *Results, id string) *Result {
	for i := range r.Results {
		if r.Results[i].ID == id {
			return &r.Results[i]
		}
	}
	return nil
}

func TestParseMediansSizesAndCatalogue(t *testing.T) {
	r := Parse(sampleRaw, cat(t))
	if len(r.Results) != 5 {
		t.Fatalf("got %d results, want 5 (only BenchmarkOp_ lines): %+v", len(r.Results), r.Results)
	}
	list := byID(r, "cli.list@100")
	if list == nil || list.Runs != 3 || list.MedianMS != 100 || list.MinMS != 90 || list.MaxMS != 110 {
		t.Fatalf("cli.list@100 = %+v, want 3 runs, median 100, min 90, max 110", list)
	}
	if list.AllocsPerOp != nil {
		t.Errorf("exec benchmark reported allocs %v, want null", *list.AllocsPerOp)
	}
	if list.TargetMS == nil || *list.TargetMS != 300 || list.Bench != "BenchmarkOp_CLIList/sessions=100" || list.Sessions != 100 {
		t.Errorf("cli.list@100 mapping = %+v", list)
	}
	render := byID(r, "tui.list_render@500")
	if render == nil || render.MedianMS != 1.6 || render.AllocsPerOp == nil || *render.AllocsPerOp != 2750 {
		t.Fatalf("list render = %+v, want median 1.6 ms (even count averages), allocs 2750", render)
	}
	if *render.TargetMS != 33 {
		t.Errorf("per-size target = %v, want 33", *render.TargetMS)
	}
	ev := byID(r, "cli.events.publish")
	if ev == nil || ev.Sessions != 0 || ev.MedianMS != 1017.05 {
		t.Errorf("events publish = %+v, want unsized, 1017.05 ms", ev)
	}
	hook := byID(r, "optiming.hook_off")
	if hook == nil || hook.MedianMS != 0.0000005 {
		t.Errorf("hook off = %+v, want 0.0000005 ms", hook)
	}
	unknown := byID(r, "BenchmarkOp_Unknown@10")
	if unknown == nil || unknown.TargetMS != nil {
		t.Errorf("uncatalogued bench = %+v, want op = bench name, no target", unknown)
	}
	if len(r.Skipped) != 1 || r.Skipped[0] != "BenchmarkOp_CLISessionStart (skip)" {
		t.Errorf("skipped = %v", r.Skipped)
	}
}

const sampleDoc = `# Performance

Prose the docs agent wrote. Keep me.

## Current numbers

<!-- BENCH:BEGIN -->
old table
<!-- BENCH:END -->

Between the blocks.

## Slowest operations vs target

<!-- BACKLOG:BEGIN -->
old list
<!-- BACKLOG:END -->
Trailing prose.
`

func results(label, date string, list, events float64) *Results {
	t300, t100 := 300.0, 100.0
	return &Results{Label: label, SHA: "0123456789abcdef", Date: date, Host: "test", Results: []Result{
		{ID: "cli.list@100", Bench: "BenchmarkOp_CLIList/sessions=100", Op: "cli.list", Sessions: 100, Runs: 5, MedianMS: list, TargetMS: &t300},
		{ID: "cli.events.publish", Bench: "BenchmarkOp_CLIEventsPublish", Op: "cli.events.publish", Runs: 5, MedianMS: events, TargetMS: &t100},
	}}
}

func TestRenderTableDeltaVerdictAndBacklog(t *testing.T) {
	newer := results("branch", "2026-10-20T04:00:00+02:00", 120, 1010)
	older := results("1.16.31", "2026-10-19T04:00:00+02:00", 100, 1000)
	older.CommitDate = "2026-10-18T00:00:00+02:00"
	newer.CommitDate = "2026-10-19T23:00:00+02:00"
	out, err := Render(sampleDoc, cat(t), []*Results{newer, older})
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"Prose the docs agent wrote. Keep me.", "Between the blocks.", "Trailing prose.\n", "## Current numbers", "## Slowest operations vs target"} {
		if !strings.Contains(out, keep) {
			t.Errorf("text outside the markers lost: %q", keep)
		}
	}
	if strings.Contains(out, "old table") || strings.Contains(out, "old list") {
		t.Error("old marker contents survived")
	}
	if !strings.Contains(out, "| 1.16.31 | branch |") {
		t.Errorf("columns not ordered oldest to newest:\n%s", out)
	}
	if !strings.Contains(out, "| CLI list (`cli.list`) | 100 | 300 ms | 100 | 120 | +20% | ok |") {
		t.Errorf("cli.list row wrong:\n%s", out)
	}
	if !strings.Contains(out, "| Events publish (`cli.events.publish`) | - | 100 ms | 1000 | 1010 | +1% | **over** (10.1x) |") {
		t.Errorf("events row wrong:\n%s", out)
	}
	if !strings.Contains(out, "1. **Events publish** (`cli.events.publish`): 1010 ms vs 100 ms target, 10.1x. Flush waits for the 1 s sync tick.") {
		t.Errorf("backlog first item wrong:\n%s", out)
	}
	if strings.Contains(out, "2. **CLI list**") {
		t.Errorf("backlog lists an operation under its target:\n%s", out)
	}
	// Rendering twice is stable.
	again, err := Render(out, cat(t), []*Results{newer, older})
	if err != nil || again != out {
		t.Errorf("render not idempotent (err %v)", err)
	}
}

func TestRenderMissingColumnAndMarkers(t *testing.T) {
	a := results("a", "2026-10-19T00:00:00Z", 100, 1000)
	b := results("b", "2026-10-20T00:00:00Z", 100, 1000)
	b.Results = b.Results[:1] // events missing in the newest run
	out, err := Render(sampleDoc, cat(t), []*Results{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "| 1000 | n/a | n/a | no data |") {
		t.Errorf("missing newest value not shown as n/a / no data:\n%s", out)
	}
	if _, err := Render("# no markers\n", cat(t), []*Results{a}); err == nil {
		t.Error("render without markers succeeded, want error")
	}
}

func TestFmtMS(t *testing.T) {
	for in, want := range map[float64]string{0.0000005: "0.5 ns", 0.342: "0.342", 1.83: "1.83", 31.76: "31.8", 1514.4: "1514"} {
		if got := fmtMS(in); got != want {
			t.Errorf("fmtMS(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRenderCommandsEndToEnd(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	catPath := write("catalogue.json", sampleCatalogue)
	raw := write("raw.txt", sampleRaw)
	doc := write("performance.md", sampleDoc)
	resDir := filepath.Join(dir, "results")
	if err := runParse([]string{"-in", raw, "-out", filepath.Join(resDir, "x.json"), "-label", "x", "-ref", "HEAD", "-sha", "abc", "-catalogue", catPath, "-host", "h"}); err != nil {
		t.Fatal(err)
	}
	var r Results
	data, err := os.ReadFile(filepath.Join(resDir, "x.json"))
	if err != nil || json.Unmarshal(data, &r) != nil || r.Label != "x" || r.Date == "" || len(r.Results) != 5 {
		t.Fatalf("results file = %+v (err %v)", r, err)
	}
	if err := runRender([]string{"-results", resDir, "-catalogue", catPath, "-doc", doc}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(doc)
	if !strings.Contains(string(got), "| TUI list render (`tui.list_render`) | 500 | 33 ms | 1.60 | n/a | ok |") {
		t.Errorf("rendered doc:\n%s", got)
	}
	if err := runParse([]string{"-in", write("empty.txt", "PASS\n"), "-out", filepath.Join(dir, "e.json"), "-label", "e", "-catalogue", catPath}); err == nil {
		t.Error("parse of output without benchmarks succeeded, want error")
	}
}
