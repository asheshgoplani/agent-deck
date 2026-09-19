package reader

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

func opencodeTree(t *testing.T) string {
	t.Helper()
	storage, err := testcorpus.OpenCodeTree(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return storage
}

func TestOpenCodeDiscoverAndIngest(t *testing.T) {
	storage := opencodeTree(t)
	// The newest file in the tree is a part: its mtime must win.
	newest := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(storage, "part", "msg_b", "prt_4.json"), newest, newest); err != nil {
		t.Fatal(err)
	}
	var refs []SourceRef
	if err := (OpenCode{}).Discover(context.Background(), []Root{{Harness: HarnessOpenCode, Dir: storage}}, func(r SourceRef) error { refs = append(refs, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs: %+v", refs)
	}
	ref := refs[0]
	if ref.NativeID != "ses_41edf7088ffezWqCkbEE66oiGT" || ref.Aux != storage {
		t.Fatalf("ref: %+v", ref)
	}
	sessInfo, _ := os.Stat(ref.Path)
	if ref.Size <= sessInfo.Size() || ref.MtimeNS < newest.UnixNano()-int64(time.Second) {
		t.Fatalf("size %d (session json %d) mtime %d want the tree total and the newest part", ref.Size, sessInfo.Size(), ref.MtimeNS)
	}

	rec := newRecorder()
	to, err := (OpenCode{}).Ingest(context.Background(), ref, 0, rec, nil)
	if err != nil || to != ref.Size {
		t.Fatalf("to %d (size %d) err %v", to, ref.Size, err)
	}
	var native, cwd, title, model string
	for _, s := range rec.sessions {
		native = firstNonEmpty(s.NativeID, native)
		cwd = firstNonEmpty(s.CWD, cwd)
		title = firstNonEmpty(s.Title, title)
		model = firstNonEmpty(s.Model, model)
	}
	if native != "ses_41edf7088ffezWqCkbEE66oiGT" || cwd != "/Users/x/claude-deck" || title != "Greeting and quick check-in" || model != "big-pickle" {
		t.Fatalf("session %q %q %q %q", native, cwd, title, model)
	}
	if len(rec.msgs) != 2 || rec.msgs[0].Role != recall.RoleUser || rec.msgs[1].Text != "The root cause was clock skew." || rec.msgs[1].TS != 1769133412 {
		t.Fatalf("msgs: %+v", rec.msgs)
	}
	if len(rec.calls) != 1 || rec.calls[0].Name != "codesearch" || !rec.calls[0].IsError || rec.calls[0].DurationMS != 500 || rec.calls[0].ArgDigest != "clock skew retries" {
		t.Fatalf("calls: %+v", rec.calls)
	}
	if len(rec.usage) != 1 || rec.usage[0].In != 20935 || rec.usage[0].CacheR != 32 {
		t.Fatalf("usage: %+v", rec.usage)
	}
	if CursorOf(OpenCode{}) != CursorNone {
		t.Fatal("OpenCode must reparse whole on change")
	}
}
