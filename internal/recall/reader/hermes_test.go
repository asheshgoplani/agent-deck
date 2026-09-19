package reader

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

func hermesDB(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if _, err := testcorpus.HermesHome(home); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestHermesDiscoverAndIngest(t *testing.T) {
	home := hermesDB(t)
	var refs []SourceRef
	if err := (Hermes{}).Discover(context.Background(), []Root{{Harness: HarnessHermes, Dir: home}, {Harness: HarnessHermes, Dir: home}}, func(r SourceRef) error { refs = append(refs, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs: %+v", refs)
	}
	first := refs[0]
	if first.NativeID != "20260814_101329_7730e5" || first.Size != 4 || first.Path != filepath.Join(home, hermesStateDB)+"#20260814_101329_7730e5" || first.MtimeNS != 1786695216*1e9 {
		t.Fatalf("first ref: %+v", first)
	}
	if refs[1].Size != 6 {
		t.Fatalf("second ref size %d want the highest message id", refs[1].Size)
	}
	if CursorOf(Hermes{}) != CursorOpaque {
		t.Fatal("Hermes cursor must be opaque (a row id)")
	}

	rec := newRecorder()
	to, err := (Hermes{}).Ingest(context.Background(), first, 0, rec, nil)
	if err != nil || to != 4 {
		t.Fatalf("to %d err %v", to, err)
	}
	var native, cwd, branch, title, model string
	for _, s := range rec.sessions {
		native = firstNonEmpty(s.NativeID, native)
		cwd = firstNonEmpty(s.CWD, cwd)
		branch = firstNonEmpty(s.Branch, branch)
		title = firstNonEmpty(s.Title, title)
		model = firstNonEmpty(s.Model, model)
	}
	if native != "20260814_101329_7730e5" || cwd != "/Users/x" || branch != "main" || title != "Friendly greeting #2" || model != "gpt-5.6-sol" {
		t.Fatalf("session %q %q %q %q %q", native, cwd, branch, title, model)
	}
	if len(rec.msgs) != 3 || rec.msgs[0].Role != recall.RoleUser || rec.msgs[1].ToolNames[0] != "terminal" || rec.msgs[2].Text != "Hi! How can I help?" {
		t.Fatalf("msgs: %+v", rec.msgs)
	}
	if len(rec.calls) != 1 || rec.calls[0].Name != "terminal" || !rec.calls[0].IsError || rec.calls[0].DurationMS != 500 || rec.calls[0].ArgDigest != "ls" {
		t.Fatalf("calls: %+v", rec.calls)
	}
	if len(rec.usage) != 1 || rec.usage[0].In != 5985 || rec.usage[0].Out != 11 {
		t.Fatalf("usage: %+v", rec.usage)
	}
	// Resume from the cursor: nothing new.
	rec2 := newRecorder()
	if to, err := (Hermes{}).Ingest(context.Background(), first, 4, rec2, nil); err != nil || to != 4 || len(rec2.msgs) != 0 || len(rec2.usage) != 0 {
		t.Fatalf("resume: to %d msgs %d usage %d err %v", to, len(rec2.msgs), len(rec2.usage), err)
	}
	// The forked child carries its parent.
	rec3 := newRecorder()
	if _, err := (Hermes{}).Ingest(context.Background(), refs[1], 0, rec3, nil); err != nil {
		t.Fatal(err)
	}
	if rec3.sessions[0].ForkOf != "20260814_101329_7730e5" {
		t.Fatalf("fork: %+v", rec3.sessions[0])
	}
	// The database is never written.
	for _, name := range []string{hermesStateDB + "-wal", hermesStateDB + "-journal"} {
		if _, err := os.Stat(filepath.Join(home, name)); err == nil {
			t.Fatalf("%s exists: the reader wrote to Hermes's database", name)
		}
	}
}
