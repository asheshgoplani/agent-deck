package reader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

const piID = testcorpus.PiID

const piShapes = testcorpus.PiShapes

func TestPiIngest_AllRecordKinds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-08-23T22-51-45-235Z_"+piID+".jsonl")
	if err := os.WriteFile(path, []byte(piShapes), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := newRecorder()
	to, err := (Pi{}).Ingest(context.Background(), SourceRef{Harness: HarnessPi, Path: path, NativeID: "from-filename"}, 0, rec, nil)
	if err != nil || to != int64(len(piShapes)) {
		t.Fatalf("to %d err %v", to, err)
	}
	var native, cwd, forkOf, title, model string
	for _, s := range rec.sessions {
		native = firstNonEmpty(s.NativeID, native)
		cwd = firstNonEmpty(s.CWD, cwd)
		forkOf = firstNonEmpty(s.ForkOf, forkOf)
		title = firstNonEmpty(s.Title, title)
		model = firstNonEmpty(s.Model, model)
	}
	if native != piID || cwd != "/private/tmp/phase2-exp" || forkOf != "01a030d2-6805-77ca-86fb-7d759f7258ac" || title != "hermes-eval" || model != "gpt-5.6-sol" {
		t.Fatalf("session %q %q fork %q title %q model %q", native, cwd, forkOf, title, model)
	}
	// prompt, assistant text, compaction summary, string prompt. Thinking,
	// tool results and custom messages are never indexed.
	if len(rec.msgs) != 4 {
		t.Fatalf("msgs: %+v", rec.msgs)
	}
	for _, m := range rec.msgs {
		if strings.Contains(strings.ToLower(m.Text), "zebra") {
			t.Fatalf("non-conversation text indexed: %q", m.Text)
		}
	}
	if rec.msgs[1].Role != recall.RoleAssistant || rec.msgs[1].Text != "Reading the README first." || len(rec.msgs[1].ToolNames) != 1 {
		t.Fatalf("assistant msg: %+v", rec.msgs[1])
	}
	if !rec.msgs[2].IsCompact || rec.msgs[3].Text != "plain string prompt" {
		t.Fatalf("compaction/string prompt: %+v %+v", rec.msgs[2], rec.msgs[3])
	}
	if len(rec.calls) != 2 {
		t.Fatalf("calls: %+v", rec.calls)
	}
	if c := rec.calls[0]; c.Name != "read" || c.IsError || c.DurationMS != 1250 || c.ArgDigest != "/Users/x/README.md" || len(c.Touches) != 0 {
		t.Fatalf("read call: %+v", c)
	}
	if c := rec.calls[1]; c.Name != "bash" || !c.IsError || c.DurationMS != 500 || c.ArgDigest != "ls notes" {
		t.Fatalf("bash call: %+v", c)
	}
	if len(rec.usage) != 2 || rec.usage[0].In != 5800 || rec.usage[0].Out != 186 {
		t.Fatalf("usage: %+v", rec.usage)
	}
	if rec.counts[CountCompact] != 1 || rec.counts[CountUnknownType] != 1 {
		t.Fatalf("counts: %+v", rec.counts)
	}
}

func TestPiDiscover_SessionsAndAgentDeckDirs(t *testing.T) {
	home := t.TempDir()
	a := filepath.Join(home, "agent", "sessions", "--private-tmp-x--")
	b := filepath.Join(home, "agent-deck", "22fe4ef2-1787227810")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(a, "2026-08-23T22-51-45-235Z_"+piID+".jsonl"):                             piID,
		filepath.Join(b, "2026-08-20T12-10-11-517Z_01a01f14-2ebd-72bf-8f99-1e101065f30a.jsonl"): "01a01f14-2ebd-72bf-8f99-1e101065f30a",
	}
	for p := range files {
		if err := os.WriteFile(p, []byte(piShapes), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(a, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	err := (Pi{}).Discover(context.Background(), []Root{{Harness: HarnessPi, Dir: home}}, func(r SourceRef) error {
		got[r.Path] = r.NativeID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("discovered %+v", got)
	}
	for p, id := range files {
		if got[p] != id {
			t.Fatalf("%s: native %q want %q", p, got[p], id)
		}
	}
}
