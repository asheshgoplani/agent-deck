package tmux

import (
	"os"
	"reflect"
	"path/filepath"
	"testing"
)

func TestParsePaneStatus(t *testing.T) {
	cases := []struct {
		file string
		want PaneStatus
	}{
		{"claude-working.txt", PaneStatus{Running: true, Verb: "Symbioting…", Elapsed: "1m 34s", Tokens: "↓ 3.7k tokens",
			Footer: "[personal] user@host:/work/project | [Fable 5.1] ctx:5% in:52.4k out:376 5h:40% 7d:20%",
			Mode:   "bypass permissions on", Notice: "✘ Auto-update failed · Run claude doctor"}},
		{"claude-running-tool.txt", PaneStatus{Running: true, Verb: "Cogitating…", Elapsed: "12s", Tokens: "↓ 1.2k tokens",
			CurrentTool: "Running go build…", Queued: 1, Mode: "plan mode on"}},
		{"claude-idle.txt", PaneStatus{Footer: "[personal] user@host:/work/sb | [F…", Mode: "bypass permissions on"}},
		{"codex-queued.txt", PaneStatus{Running: true, Verb: "Working", Elapsed: "30m 26s", CurrentTool: "1 background terminal running", Queued: 2,
			Footer: "gpt-6-sol · /work/v4 · Context 50% left · Context 50% used · weekly 87% left · 258K window · Main [default]"}},
		{"codex-idle.txt", PaneStatus{Footer: "gpt-6-sol · /work/sb · Context 100% left · Context 0% used · weekly 87% left"}},
	}
	for _, tc := range cases {
		data, err := os.ReadFile(filepath.Join("testdata", "pane_status", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		got := ParsePaneStatus(string(data))
		got.QueuedText, got.AutoCompactPct = nil, nil
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.file, got, tc.want)
		}
	}
}

// TestParsePaneStatusCorpus runs the parser over the status-audit corpus:
// it never panics, and a pane the corpus labels idle ("for Ns · done")
// with no spinner line is never reported running.
func TestParsePaneStatusCorpus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "status_corpus", "*.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("corpus: %v %d", err, len(files))
	}
	running := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		st := ParsePaneStatus(string(data))
		if st.Running {
			running++
			if st.Verb == "" {
				t.Errorf("%s: running without a verb: %+v", filepath.Base(f), st)
			}
		}
	}
	t.Logf("%d of %d corpus panes parsed as running", running, len(files))
}

func TestPaneStatusQueuedTextAutoCompactAndFacts(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "pane_status", "codex-queued.txt"))
	if err != nil {
		t.Fatal(err)
	}
	st := ParsePaneStatus(string(data))
	if len(st.QueuedText) != 2 || st.QueuedText[1] != "and a second one" {
		t.Fatalf("queued text: %q", st.QueuedText)
	}
	facts := FooterFacts(st.Footer)
	if facts["model"] != "gpt-6-sol" || facts["weekly_left"] != "87%" || facts["context_left"] != "50%" || facts["window"] != "258K" || facts["cwd"] != "/work/v4" {
		t.Fatalf("codex facts: %v", facts)
	}
	claude := FooterFacts("[personal] user@host:/work/project | [Fable 5.1] ctx:5% in:52.4k out:376 5h:40% 7d:20%")
	if claude["account"] != "personal" || claude["model"] != "Fable 5.1" || claude["5h"] != "40%" || claude["7d"] != "20%" || claude["ctx"] != "5%" || claude["cwd"] != "/work/project" {
		t.Fatalf("claude facts: %v", claude)
	}
	ac := ParsePaneStatus("✻ Cogitating… (5s)\n  ⎿  Tip: x\n                                   8% until auto-compact\n")
	if ac.AutoCompactPct == nil || *ac.AutoCompactPct != 8 {
		t.Fatalf("auto-compact: %v", ac.AutoCompactPct)
	}
	for in, want := range map[string]int{"1m 34s": 94, "12s": 12, "1h 2m 3s": 3723, "30m 26s": 1826, "": 0} {
		if got := ElapsedSeconds(in); got != want {
			t.Errorf("ElapsedSeconds(%q) = %d, want %d", in, got, want)
		}
	}
}
