package reader

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

const geminiShapes = testcorpus.GeminiShapes

// errSinkRefused stands in for a fatal store error in the sink.
var errSinkRefused = errors.New("sink refused")

func TestGeminiIngest_StreamsTheDocument(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tmp", "1ac57b4f", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-2026-01-19T12-18-196a60d9.json")
	if err := os.WriteFile(path, []byte(geminiShapes), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := newRecorder()
	ref := SourceRef{Harness: HarnessGemini, Path: path, Size: int64(len(geminiShapes)), NativeID: "session-2026-01-19T12-18-196a60d9"}
	to, err := (Gemini{}).Ingest(context.Background(), ref, 0, rec, nil)
	if err != nil || to != ref.Size {
		t.Fatalf("to %d err %v", to, err)
	}
	var native, title, model string
	for _, s := range rec.sessions {
		native = firstNonEmpty(s.NativeID, native)
		title = firstNonEmpty(s.Title, title)
		model = firstNonEmpty(s.Model, model)
	}
	if native != "session-2026-01-19T12-18-196a60d9" || title != "Analyze the flaky auth test." || model != "gemini-3-flash-preview" {
		t.Fatalf("session %q title %q model %q", native, title, model)
	}
	if len(rec.msgs) != 4 {
		t.Fatalf("msgs: %+v", rec.msgs)
	}
	for _, m := range rec.msgs {
		if strings.Contains(m.Text, "zebra") {
			t.Fatalf("thoughts or tool output indexed: %q", m.Text)
		}
	}
	if rec.msgs[2].Text != "Your task is to answer the following question\nabout clock skew" || rec.msgs[2].Role != recall.RoleUser {
		t.Fatalf("part-list content: %+v", rec.msgs[2])
	}
	// Spans are real file offsets of each message object.
	data, _ := os.ReadFile(path)
	for _, m := range rec.msgs {
		var back struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(data[m.RecOff:m.RecOff+m.RecLen], &back); err != nil || back.ID != m.UUID {
			t.Fatalf("span %d+%d of %s does not decode to the message: %v %q", m.RecOff, m.RecLen, m.UUID, err, data[m.RecOff:m.RecOff+m.RecLen])
		}
	}
	if len(rec.calls) != 2 {
		t.Fatalf("calls: %+v", rec.calls)
	}
	if c := rec.calls[0]; c.Name != "read_file" || c.IsError || c.ArgDigest != ".github-issue-context.json" || c.DurationMS != 500 {
		t.Fatalf("read_file call: %+v", c)
	}
	if c := rec.calls[1]; c.Name != "run_shell_command" || !c.IsError || c.ArgDigest != "go test ./internal/auth/" {
		t.Fatalf("shell call: %+v", c)
	}
	if len(rec.usage) != 2 || rec.usage[0].In != 10387 || rec.usage[0].Out != 30 || rec.usage[0].CacheR != 5 {
		t.Fatalf("usage: %+v", rec.usage)
	}
	if rec.counts[CountInterrupt] != 1 || rec.counts[CountAPIError] != 1 || rec.counts[CountUnknownType] != 1 {
		t.Fatalf("counts: %+v", rec.counts)
	}
}

// TestGeminiIngest_LargeFileUnderRSSCap streams a 32 MB document shaped
// like the largest one on the design machine (31,636,072 B, six
// messages, one of 10.5 MB of text and one whose 10 MB displayContent
// sits beside a 24-character prompt): the resident set may grow by at
// most geminiRSSCapBytes, a message whose text is itself over the cap is
// skipped and counted, and a message with an oversize sibling field keeps
// its text (the phase-3 review's finding 8).
const geminiRSSCapBytes = 16 << 20

func TestGeminiIngest_LargeFileUnderRSSCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-2026-03-06T10-44-df8dac27.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"sessionId":"df8dac27","projectHash":"videos","startTime":"2026-03-06T10:44:00.000Z","lastUpdated":"2026-03-06T11:44:00.000Z","messages":[`)
	chunk := strings.Repeat("the video shows a lecture about clock skew and retries ", 20) // ~1.1 KB
	msg := func(id, typ string, kb int) {
		f.WriteString(`{"id":"` + id + `","timestamp":"2026-03-06T10:45:00.000Z","type":"` + typ + `","content":"`)
		for w := 0; w < kb; w++ {
			f.WriteString(chunk)
		}
		f.WriteString(`"}`)
	}
	msg("u1", "user", 4)
	f.WriteString(",")
	msg("g1", "gemini", 10*1024) // ~10.5 MB: above MaxLineBytes
	f.WriteString(",")
	msg("u2", "user", 8)
	f.WriteString(",")
	msg("g2", "gemini", 9*1024) // ~10 MB more
	f.WriteString(",")
	msg("u3", "user", 2)
	f.WriteString(",")
	// The real shape of the 31.6 MB file: a short prompt beside inline video.
	f.WriteString(`{"id":"u4","timestamp":"2026-03-06T10:46:00.000Z","type":"user","content":"System: Please continue.","displayContent":[{"inlineData":{"mimeType":"video/mp4","data":"`)
	for w := 0; w < 10*1024; w++ {
		f.WriteString(chunk)
	}
	f.WriteString(`"}}]}`)
	f.WriteString(`],"summary":"videos"}`)
	f.Close()
	info, _ := os.Stat(path)
	if info.Size() < 30<<20 {
		t.Fatalf("fixture is %d bytes, want ~32 MB", info.Size())
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rec := newRecorder()
	ref := SourceRef{Harness: HarnessGemini, Path: path, Size: info.Size(), NativeID: "x"}
	to, err := (Gemini{}).Ingest(context.Background(), ref, 0, rec, nil)
	runtime.ReadMemStats(&after)
	if err != nil || to != info.Size() {
		t.Fatalf("to %d err %v", to, err)
	}
	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if peak := int64(after.HeapSys) - int64(before.HeapSys); peak > geminiRSSCapBytes || growth > geminiRSSCapBytes {
		t.Fatalf("heap grew by %d B (sys %d) over a %d B file; cap %d", growth, peak, info.Size(), geminiRSSCapBytes)
	}
	t.Logf("file %d B: heap growth %d B, heap sys growth %d B", info.Size(), growth, int64(after.HeapSys)-int64(before.HeapSys))
	if len(rec.msgs) != 4 || rec.counts[CountLineTooLong] != 2 {
		t.Fatalf("msgs %d (want 4), too-long %d (want 2)", len(rec.msgs), rec.counts[CountLineTooLong])
	}
	last := rec.msgs[3]
	if last.UUID != "u4" || last.Text != "System: Please continue." {
		t.Fatalf("the prompt beside the oversize displayContent was lost: %+v", last)
	}
	// The span still covers the whole message object on disk.
	data := make([]byte, 40)
	fh, _ := os.Open(path)
	defer fh.Close()
	if _, err := fh.ReadAt(data, last.RecOff); err != nil || !strings.HasPrefix(string(data), `{"id":"u4"`) {
		t.Fatalf("span %d+%d: %v %q", last.RecOff, last.RecLen, err, data)
	}
}

// TestGeminiIngest_SinkErrorStopsThePass: a sink that refuses (a fatal
// store error, a quarantine) ends the pass at that message instead of the
// reader scanning the rest of the document for nothing (finding 9).
func TestGeminiIngest_SinkErrorStopsThePass(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-2026-01-19T12-18-196a60d9.json")
	if err := os.WriteFile(path, []byte(geminiShapes), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := newRecorder()
	rec.failMsg = errSinkRefused
	ref := SourceRef{Harness: HarnessGemini, Path: path, Size: int64(len(geminiShapes)), NativeID: "x"}
	_, err := (Gemini{}).Ingest(context.Background(), ref, 0, rec, nil)
	if err == nil || !strings.Contains(err.Error(), errSinkRefused.Error()) {
		t.Fatalf("the sink's error must end the pass: %v", err)
	}
	if rec.counts[CountUnknownType] != 0 {
		t.Fatalf("the reader kept scanning after the sink refused: counts %+v", rec.counts)
	}
}

func TestGeminiDiscover_ChatsOnly(t *testing.T) {
	home := t.TempDir()
	chats := filepath.Join(home, "tmp", "abc", "chats")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chats, "session-2026-01-19T12-18-196a60d9.json"), []byte(geminiShapes), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "tmp", "abc", "session-elsewhere.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var refs []SourceRef
	if err := (Gemini{}).Discover(context.Background(), []Root{{Harness: HarnessGemini, Dir: home}}, func(r SourceRef) error { refs = append(refs, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].NativeID != "session-2026-01-19T12-18-196a60d9" {
		t.Fatalf("refs: %+v", refs)
	}
}
