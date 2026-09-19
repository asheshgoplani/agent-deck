package reader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

const (
	codexThread = testcorpus.CodexThread
	codexShapes = testcorpus.CodexShapes
)

// codexHome lays out a Codex home with one rollout, a title index and,
// when cursor >= 0, a projection database with that cursor for the thread.
func codexHome(t *testing.T, cursor int64) (home, rollout string) {
	t.Helper()
	home = t.TempDir()
	rollout, err := testcorpus.CodexHome(home, cursor)
	if err != nil {
		t.Fatal(err)
	}
	return home, rollout
}

func codexRef(home, path string) SourceRef {
	info, _ := os.Stat(path)
	return SourceRef{Harness: HarnessCodex, Path: path, Size: info.Size(), MtimeNS: info.ModTime().UnixNano(), NativeID: codexThread, Aux: home}
}

func TestCodexIngest_AllRecordKinds(t *testing.T) {
	home, rollout := codexHome(t, -1)
	rec := newRecorder()
	to, err := (Codex{}).Ingest(context.Background(), codexRef(home, rollout), 0, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if to != int64(len(codexShapes)) {
		t.Fatalf("parsedTo %d want %d (no projection: tailed to size)", to, len(codexShapes))
	}
	// Session: id, cwd, version, branch from session_meta; title from
	// session_index.jsonl; model from turn_context.
	var native, cwd, version, branch, title, titleSrc, model string
	for _, s := range rec.sessions {
		native = firstNonEmpty(s.NativeID, native)
		cwd = firstNonEmpty(s.CWD, cwd)
		version = firstNonEmpty(s.Version, version)
		branch = firstNonEmpty(s.Branch, branch)
		model = firstNonEmpty(s.Model, model)
		if s.Title != "" {
			title, titleSrc = s.Title, s.TitleSrc
		}
	}
	if native != codexThread || cwd != "/Users/x/proj" || version != "0.154.0" || branch != "perf/refresh-tick-v2" || model != "gpt-5.6-sol" {
		t.Fatalf("session: %q %q %q %q %q", native, cwd, version, branch, model)
	}
	if title != "Fix flaky auth" || titleSrc != "thread_name" {
		t.Fatalf("title %q/%q", title, titleSrc)
	}
	// Messages: the environment context (meta), the prompt, the answer, the
	// compaction summary, the follow-up. Developer text and reasoning are
	// never indexed; replacement_history is not re-emitted.
	var texts []string
	for _, m := range rec.msgs {
		texts = append(texts, m.Text)
	}
	joined := strings.Join(texts, "\n")
	if strings.Contains(joined, "zebra") {
		t.Fatalf("developer/reasoning text indexed: %q", joined)
	}
	if len(rec.msgs) != 5 {
		t.Fatalf("msgs = %d: %q", len(rec.msgs), texts)
	}
	if !rec.msgs[0].IsMeta || rec.msgs[0].Role != recall.RoleUser {
		t.Fatalf("environment context must be meta user text: %+v", rec.msgs[0])
	}
	if rec.msgs[1].Text != "Fix the flaky auth test, the clock skew one" || rec.msgs[2].Role != recall.RoleAssistant {
		t.Fatalf("prompt/answer: %+v %+v", rec.msgs[1], rec.msgs[2])
	}
	if c := rec.msgs[3]; !c.IsCompact || !c.SupersedesPrior || !strings.HasPrefix(c.Text, "Summary so far") {
		t.Fatalf("compacted must be a superseding compact summary: %+v", c)
	}
	if rec.msgs[4].SupersedesPrior || rec.msgs[4].Text != "Now add a regression test" {
		t.Fatalf("post-compaction prompt: %+v", rec.msgs[4])
	}
	// Spans point at the record's own bytes.
	data, _ := os.ReadFile(rollout)
	for _, m := range rec.msgs {
		line := string(data[m.RecOff : m.RecOff+m.RecLen])
		if !strings.HasSuffix(line, "\n") || !strings.Contains(line, strings.SplitN(m.Text, "\n", 2)[0][:10]) {
			t.Fatalf("span %d+%d does not cover the record: %q", m.RecOff, m.RecLen, line)
		}
	}
	// Tool calls: shell (error, 2.5 s) and exec (ok, 250 ms).
	if len(rec.calls) != 2 {
		t.Fatalf("calls: %+v", rec.calls)
	}
	if c := rec.calls[0]; c.Name != "shell" || !c.IsError || c.DurationMS != 2500 || !strings.Contains(c.ArgDigest, "go") {
		t.Fatalf("shell call: %+v", c)
	}
	if c := rec.calls[1]; c.Name != "exec" || c.IsError || c.DurationMS != 250 || !strings.Contains(c.ArgDigest, "sed -n") {
		t.Fatalf("exec call: %+v", c)
	}
	// Usage from token_usage_record.usage (never the thread total).
	if len(rec.usage) != 1 || rec.usage[0].In != 25088 || rec.usage[0].Out != 6 || rec.usage[0].CacheR != 12928 || rec.usage[0].Model != "gpt-5.6-sol" {
		t.Fatalf("usage: %+v", rec.usage)
	}
	if rec.counts[CountCompact] != 1 || rec.counts[CountInterrupt] != 1 || rec.counts[CountUnknownType] != 1 {
		t.Fatalf("counts: %+v", rec.counts)
	}
}

// TestCodexIngest_ProjectionCursorBoundsThePass: parsed_to = min(cursor,
// size) while the file is fresh; a stale file is tailed to its size.
func TestCodexIngest_ProjectionCursorBoundsThePass(t *testing.T) {
	// The cursor lands exactly on a line boundary after the first prompt.
	lines := strings.SplitAfter(codexShapes, "\n")
	cursor := int64(len(strings.Join(lines[:6], "")))
	size := int64(len(codexShapes))

	t.Run("cursor below size", func(t *testing.T) {
		home, rollout := codexHome(t, cursor)
		rec := newRecorder()
		to, err := (Codex{}).Ingest(context.Background(), codexRef(home, rollout), 0, rec, nil)
		if err != nil || to != cursor {
			t.Fatalf("parsedTo %d want %d (err %v)", to, cursor, err)
		}
		if len(rec.msgs) != 2 || rec.msgs[1].Text != "Fix the flaky auth test, the clock skew one" {
			t.Fatalf("msgs past the cursor were indexed: %+v", rec.msgs)
		}
		// Resuming from the cursor picks up the rest once Codex moves it.
		rec2 := newRecorder()
		ref := codexRef(home, rollout)
		ref.Aux = "" // no projection: plain tailing
		to, err = (Codex{}).Ingest(context.Background(), ref, cursor, rec2, nil)
		if err != nil || to != size || len(rec2.msgs) != 3 {
			t.Fatalf("resume: to %d want %d, msgs %d, err %v", to, size, len(rec2.msgs), err)
		}
	})
	t.Run("cursor above size", func(t *testing.T) {
		home, rollout := codexHome(t, size+4096)
		to, err := (Codex{}).Ingest(context.Background(), codexRef(home, rollout), 0, newRecorder(), nil)
		if err != nil || to != size {
			t.Fatalf("parsedTo %d want size %d (err %v)", to, size, err)
		}
	})
	t.Run("stale file ignores a lagging cursor", func(t *testing.T) {
		home, rollout := codexHome(t, cursor)
		old := time.Now().Add(-2 * projectionGrace)
		if err := os.Chtimes(rollout, old, old); err != nil {
			t.Fatal(err)
		}
		to, err := (Codex{}).Ingest(context.Background(), codexRef(home, rollout), 0, newRecorder(), nil)
		if err != nil || to != size {
			t.Fatalf("parsedTo %d want size %d (err %v)", to, size, err)
		}
	})
	t.Run("no projection row", func(t *testing.T) {
		home, rollout := codexHome(t, -1)
		to, err := (Codex{}).Ingest(context.Background(), codexRef(home, rollout), 0, newRecorder(), nil)
		if err != nil || to != size {
			t.Fatalf("parsedTo %d want size %d (err %v)", to, size, err)
		}
	})
}

// TestCodexIngest_EmptyCompactionHasNoRowAndNoSession pins the phase-3
// review's finding 7: on real rollouts every compacted record carries an
// empty message, so it counts and supersedes but is never stored as an
// empty compact_summary, and a rollout holding only session_meta and
// compactions (Codex writes one when a thread resumes after compaction)
// produces no session at all.
func TestCodexIngest_EmptyCompactionHasNoRowAndNoSession(t *testing.T) {
	const emptySummary = `"message":""`
	withEmpty := strings.Replace(codexShapes, `"message":"Summary so far: the auth test flakes on clock skew."`, emptySummary, 1)
	if withEmpty == codexShapes {
		t.Fatal("fixture drift: the compaction summary text moved")
	}
	t.Run("full rollout", func(t *testing.T) {
		home, rollout := codexHome(t, -1)
		if err := os.WriteFile(rollout, []byte(withEmpty), 0o644); err != nil {
			t.Fatal(err)
		}
		rec := newRecorder()
		if _, err := (Codex{}).Ingest(context.Background(), codexRef(home, rollout), 0, rec, nil); err != nil {
			t.Fatal(err)
		}
		if rec.counts[CountCompact] != 1 {
			t.Fatalf("compact count %d", rec.counts[CountCompact])
		}
		var compacts, empty int
		for _, m := range rec.msgs {
			if m.IsCompact {
				compacts++
			}
			if strings.TrimSpace(m.Text) == "" {
				empty++
			}
		}
		// The reader still hands the superseding record to the sink (which
		// records the edge and stores nothing); no other message is empty.
		if compacts != 1 || empty != 1 || len(rec.msgs) != 5 {
			t.Fatalf("msgs: compacts %d empty %d total %d", compacts, empty, len(rec.msgs))
		}
	})
	t.Run("compaction-only rollout", func(t *testing.T) {
		lines := strings.SplitAfter(withEmpty, "\n")
		var only strings.Builder
		for _, l := range lines {
			if strings.Contains(l, `"type":"session_meta"`) || strings.Contains(l, `"type":"turn_context"`) || strings.Contains(l, `"type":"compacted"`) {
				only.WriteString(l)
			}
		}
		home, rollout := codexHome(t, -1)
		if err := os.WriteFile(rollout, []byte(only.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		rec := newRecorder()
		to, err := (Codex{}).Ingest(context.Background(), codexRef(home, rollout), 0, rec, nil)
		if err != nil || to != int64(only.Len()) {
			t.Fatalf("to %d err %v", to, err)
		}
		for _, s := range rec.sessions {
			if s.NativeID != "" {
				t.Fatalf("a compaction-only rollout must not open a session: %+v", rec.sessions)
			}
		}
		if len(rec.msgs) != 1 || rec.msgs[0].Text != "" || rec.counts[CountCompact] != 1 {
			t.Fatalf("msgs %+v counts %+v", rec.msgs, rec.counts)
		}
	})
}

func TestCodexDiscover_SessionsAndArchived(t *testing.T) {
	home, rollout := codexHome(t, -1)
	arch := filepath.Join(home, "archived_sessions")
	if err := os.MkdirAll(arch, 0o755); err != nil {
		t.Fatal(err)
	}
	other := "rollout-2025-12-03T12-32-36-019ae3fc-57dd-7cd3-9b99-7a96be279be4.jsonl"
	if err := os.WriteFile(filepath.Join(arch, other), []byte(codexShapes), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sessions", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var refs []SourceRef
	err := (Codex{}).Discover(context.Background(), []Root{{Harness: HarnessCodex, Profile: "personal", Dir: home}, {Harness: HarnessCodex, Dir: home}},
		func(r SourceRef) error { refs = append(refs, r); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs: %+v", refs)
	}
	byID := map[string]SourceRef{}
	for _, r := range refs {
		byID[r.NativeID] = r
	}
	if r := byID[codexThread]; r.Path != rollout || r.Aux != home || r.Profile != "personal" || r.Size != int64(len(codexShapes)) {
		t.Fatalf("rollout ref: %+v", r)
	}
	if _, ok := byID["019ae3fc-57dd-7cd3-9b99-7a96be279be4"]; !ok {
		t.Fatalf("archived rollout not discovered: %+v", refs)
	}
}
