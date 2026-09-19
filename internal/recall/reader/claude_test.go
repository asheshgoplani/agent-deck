package reader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recorder struct {
	sessions []Session
	msgs     []Msg
	calls    []ToolCall
	usage    []Usage
	counts   map[Counter]int64
	failMsg  error
}

func newRecorder() *recorder { return &recorder{counts: map[Counter]int64{}} }

func (r *recorder) Session(s Session)         { r.sessions = append(r.sessions, s) }
func (r *recorder) ToolCall(t ToolCall) error { r.calls = append(r.calls, t); return nil }
func (r *recorder) Usage(u Usage) error       { r.usage = append(r.usage, u); return nil }
func (r *recorder) Count(c Counter, n int64)  { r.counts[c] += n }
func (r *recorder) Msg(m Msg) error {
	if r.failMsg != nil {
		return r.failMsg
	}
	r.msgs = append(r.msgs, m)
	return nil
}

// realShapes are records copied (trimmed) from transcripts on the design
// machine: every kind the reader must handle.
const realShapes = `{"type":"custom-title","customTitle":"review-2308","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"type":"agent-name","agentName":"review-2308","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"type":"last-prompt","leafUuid":"258cfca9","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"Review PR 2308 for the auth fix"},"uuid":"u1","timestamp":"2026-09-19T10:00:00.000Z","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7","cwd":"/Users/x/proj","gitBranch":"main","version":"2.1.0"}
{"parentUuid":"u1","isSidechain":false,"type":"assistant","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"thinking","thinking":"secret thoughts"},{"type":"text","text":"Reading the diff."},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/Users/x/proj/auth.go"}}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":7}},"uuid":"a1","timestamp":"2026-09-19T10:00:01.000Z","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"parentUuid":"a1","isSidechain":false,"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"package auth"}]},"toolUseResult":{"stdout":"","interrupted":false},"uuid":"u2","timestamp":"2026-09-19T10:00:03.500Z","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"parentUuid":"u2","isSidechain":false,"type":"assistant","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"go test ./internal/auth/ -run TestFlaky","description":"run the flaky test"}}],"usage":{"input_tokens":1,"output_tokens":1}},"uuid":"a2","timestamp":"2026-09-19T10:00:04.000Z","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"parentUuid":"a2","isSidechain":false,"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","is_error":true,"content":"FAIL"}]},"toolUseResult":{"stdout":"","stderr":"exit 1","interrupted":true},"uuid":"u3","timestamp":"2026-09-19T10:00:09.000Z","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"parentUuid":"u3","isSidechain":false,"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]},"uuid":"u4","timestamp":"2026-09-19T10:00:10.000Z","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"parentUuid":null,"logicalParentUuid":"u4","isSidechain":false,"type":"system","subtype":"compact_boundary","content":"Conversation compacted","isMeta":false,"timestamp":"2026-09-19T10:01:00.000Z","uuid":"s1","level":"info","compactMetadata":{"trigger":"auto","preTokens":985611}}
{"parentUuid":"s1","isSidechain":false,"type":"user","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued from a previous conversation about the auth fix"},"uuid":"u5","timestamp":"2026-09-19T10:01:01.000Z","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"type":"system","subtype":"api_error","level":"error","error":{"message":"529 Overloaded","status":529},"uuid":"s2","timestamp":"2026-09-19T10:01:02.000Z"}
{"type":"system","subtype":"turn_duration","durationMs":355783,"messageCount":156,"timestamp":"2026-09-19T10:01:03.000Z","uuid":"s3"}
{"parentUuid":"u5","isSidechain":false,"attachment":{"type":"diagnostics","files":[{"uri":"file:///a.go","diagnostics":"the word zebra must not be indexed"}]},"type":"attachment","uuid":"at1","timestamp":"2026-09-19T10:01:04.000Z"}
{"type":"file-history-snapshot","messageId":"u5","snapshot":{"trackedFileBackups":{"a.go":{"content":"zebra again"}}}}
{"parentUuid":"u5","isSidechain":false,"data":{"type":"hook_progress","output":"zebra progress"},"type":"progress","uuid":"p1"}
{"type":"queue-operation","operation":"enqueue","content":"zebra queued","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"type":"pr-link","prNumber":2308,"prUrl":"https://example.invalid/pr/2308","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"type":"permission-mode","permissionMode":"default","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"type":"brand-new-record-kind","payload":1}
not json at all
{"type":"ai-title","aiTitle":"Auth fix review","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
{"parentUuid":"u5","isSidechain":false,"type":"assistant","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"Done: the root cause was clock skew."}],"usage":{"input_tokens":2,"output_tokens":2}},"uuid":"a3","timestamp":"2026-09-19T10:02:00.000Z","sessionId":"3fec37ee-811e-48c4-ba90-552abd93d9c7"}
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "3fec37ee-811e-48c4-ba90-552abd93d9c7.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClaudeIngest_AllRecordKinds(t *testing.T) {
	path := writeTemp(t, realShapes)
	rec := newRecorder()
	to, err := (Claude{}).Ingest(context.Background(), SourceRef{Path: path, NativeID: "from-filename"}, 0, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if to != int64(len(realShapes)) {
		t.Fatalf("parsedTo %d want %d", to, len(realShapes))
	}
	// Session facts: native id from the record, cwd, branch, titles in
	// order, model.
	var native, cwd, branch, model string
	var titles []string
	for _, s := range rec.sessions {
		if s.NativeID != "" {
			native, cwd, branch = s.NativeID, s.CWD, s.Branch
		}
		if s.Title != "" {
			titles = append(titles, s.TitleSrc+"="+s.Title)
		}
		if s.Model != "" {
			model = s.Model
		}
	}
	if native != "3fec37ee-811e-48c4-ba90-552abd93d9c7" || cwd != "/Users/x/proj" || branch != "main" || model != "claude-opus-5" {
		t.Fatalf("session: %q %q %q %q", native, cwd, branch, model)
	}
	if strings.Join(titles, ",") != "custom-title=review-2308,agent-name=review-2308,ai-title=Auth fix review" {
		t.Fatalf("titles = %v", titles)
	}
	// Messages: prompt, assistant text (thinking dropped), the interrupt,
	// the compact summary, the final assistant text. Tool results carry
	// no text and are not messages.
	var texts []string
	for _, m := range rec.msgs {
		texts = append(texts, m.Text)
		if strings.Contains(m.Text, "zebra") || strings.Contains(m.Text, "secret") {
			t.Fatalf("noise or thinking indexed: %q", m.Text)
		}
	}
	want := []string{"Review PR 2308 for the auth fix", "Reading the diff.", "[Request interrupted by user]",
		"This session is being continued from a previous conversation about the auth fix", "Done: the root cause was clock skew."}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("msgs = %q", texts)
	}
	if !rec.msgs[2].IsInterrupt || !rec.msgs[3].IsCompact || rec.msgs[1].Role != 2 || rec.msgs[0].Role != 1 {
		t.Fatalf("flags: %+v", rec.msgs)
	}
	if rec.msgs[0].RecOff == 0 || rec.msgs[0].RecLen == 0 || rec.msgs[0].TS != time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("offsets/ts: %+v", rec.msgs[0])
	}
	if len(rec.msgs[1].ToolNames) != 1 || rec.msgs[1].ToolNames[0] != "Read" {
		t.Fatalf("tool names: %v", rec.msgs[1].ToolNames)
	}
	// Tool calls joined to their results: Read took 2.5 s, Bash 5 s and
	// errored; the file touch is a read of auth.go.
	if len(rec.calls) != 2 {
		t.Fatalf("calls = %+v", rec.calls)
	}
	read, bash := rec.calls[0], rec.calls[1]
	if read.Name != "Read" || read.DurationMS != 2500 || read.IsError || read.ArgDigest != "/Users/x/proj/auth.go" ||
		len(read.Touches) != 1 || read.Touches[0] != (FileTouch{Path: "/Users/x/proj/auth.go", Op: "read"}) {
		t.Fatalf("read call: %+v", read)
	}
	if bash.Name != "Bash" || bash.DurationMS != 5000 || !bash.IsError || bash.ArgDigest != "go test ./internal/auth/ -run TestFlaky" || len(bash.Touches) != 0 {
		t.Fatalf("bash call: %+v", bash)
	}
	// Usage: three assistant records, cache fields carried.
	if len(rec.usage) != 3 || rec.usage[0].CacheR != 100 || rec.usage[0].CacheW != 7 || rec.usage[0].UUID != "a1" || rec.usage[0].Model != "claude-opus-5" {
		t.Fatalf("usage = %+v", rec.usage)
	}
	// Counters: one compaction, one api_error, one unknown type, one bad
	// line. Interrupts travel on the marker message's flag only (one Escape
	// press is one interrupt: the interrupted tool result before the
	// marker is the same press), so the reader emits no interrupt count.
	if rec.counts[CountCompact] != 1 || rec.counts[CountInterrupt] != 0 || rec.counts[CountAPIError] != 1 ||
		rec.counts[CountUnknownType] != 1 || rec.counts[CountBadJSON] != 1 {
		t.Fatalf("counts = %v", rec.counts)
	}
	flagged := 0
	for _, m := range rec.msgs {
		if m.IsInterrupt {
			flagged++
		}
	}
	if flagged != 1 {
		t.Fatalf("interrupt markers flagged = %d, want 1", flagged)
	}
}

func TestClaudeIngest_ResumeFromOffsetAndTornTail(t *testing.T) {
	lines := strings.SplitAfter(realShapes, "\n")
	head := strings.Join(lines[:5], "")
	path := writeTemp(t, head+`{"type":"user","message":{"role":"user","content":"torn`)
	rec := newRecorder()
	to, err := (Claude{}).Ingest(context.Background(), SourceRef{Path: path}, 0, rec, nil)
	if err != nil || to != int64(len(head)) {
		t.Fatalf("to=%d want %d err=%v", to, len(head), err)
	}
	if len(rec.msgs) != 2 {
		t.Fatalf("msgs before tail = %d", len(rec.msgs))
	}
	// Complete the file: resume from the cursor and see only the rest.
	if err := os.WriteFile(path, []byte(realShapes), 0o644); err != nil {
		t.Fatal(err)
	}
	rec2 := newRecorder()
	to2, err := (Claude{}).Ingest(context.Background(), SourceRef{Path: path}, to, rec2, nil)
	if err != nil || to2 != int64(len(realShapes)) {
		t.Fatalf("to2=%d err=%v", to2, err)
	}
	if len(rec2.msgs) != 3 || len(rec2.calls) != 2 {
		t.Fatalf("resumed pass: %d msgs %d calls", len(rec2.msgs), len(rec2.calls))
	}
	// The Read call's tool_use was in the first pass; its result arrives
	// now as an unmatched result (no name, no duration) and the first
	// pass flushed the pending call with no duration.
	if rec.calls[0].Name != "Read" || rec.calls[0].DurationMS != 0 || rec2.calls[0].Name != "" {
		t.Fatalf("cross-pass tool call: %+v / %+v", rec.calls, rec2.calls)
	}
}

func TestClaudeIngest_BudgetStopsOnALineBoundary(t *testing.T) {
	path := writeTemp(t, realShapes)
	rec := newRecorder()
	b := NewBudget(0, 600)
	to, err := (Claude{}).Ingest(context.Background(), SourceRef{Path: path}, 0, rec, b)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v", err)
	}
	if to <= 0 || to >= int64(len(realShapes)) || realShapes[to-1] != '\n' {
		t.Fatalf("parsedTo %d is not a line boundary", to)
	}
	if !b.Exhausted() || b.Consumed() != to {
		t.Fatalf("budget: consumed %d parsedTo %d", b.Consumed(), to)
	}
	rest := newRecorder()
	to2, err := (Claude{}).Ingest(context.Background(), SourceRef{Path: path}, to, rest, nil)
	if err != nil || to2 != int64(len(realShapes)) || len(rec.msgs)+len(rest.msgs) != 5 {
		t.Fatalf("continuation: to2=%d err=%v msgs %d+%d", to2, err, len(rec.msgs), len(rest.msgs))
	}
}

func TestClaudeIngest_SinkErrorStopsThePass(t *testing.T) {
	path := writeTemp(t, realShapes)
	rec := newRecorder()
	rec.failMsg = errors.New("quarantined")
	to, err := (Claude{}).Ingest(context.Background(), SourceRef{Path: path}, 0, rec, nil)
	if err == nil || err.Error() != "quarantined" || to != int64(strings.Index(realShapes, `{"parentUuid":null,"isSidechain":false,"type":"user"`)) {
		t.Fatalf("to=%d err=%v", to, err)
	}
}

func TestClaudeIngest_SidechainKeepsSourceNativeID(t *testing.T) {
	path := writeTemp(t, realShapes)
	rec := newRecorder()
	if _, err := (Claude{}).Ingest(context.Background(), SourceRef{Path: path, IsSidechain: true, NativeID: "parent/agent-1", ParentNativeID: "parent"}, 0, rec, nil); err != nil {
		t.Fatal(err)
	}
	for _, s := range rec.sessions {
		if s.NativeID != "" && s.NativeID != "parent/agent-1" {
			t.Fatalf("sidechain native id = %q", s.NativeID)
		}
	}
}

func TestClaudeDiscover_SkipsToolResultsAndCollapsesLinks(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "claude")
	proj := filepath.Join(root, "projects", "-Users-x-proj")
	sub := filepath.Join(proj, "s1", "subagents")
	for _, d := range []string{sub, filepath.Join(proj, "s1", "tool-results"), filepath.Join(root, "projects", "workflows")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(proj, "s1.jsonl"):                          "{}\n",
		filepath.Join(sub, "agent-abc.jsonl"):                    "{}\n",
		filepath.Join(sub, "agent-abc.meta.json"):                "{}",
		filepath.Join(proj, "s1", "tool-results", "big.jsonl"):   "{}\n",
		filepath.Join(root, "projects", "workflows", "wf.jsonl"): "{}\n",
		filepath.Join(proj, "notes.txt"):                         "x",
	}
	for p, c := range files {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A second root whose projects dir is a symlink to the first.
	scratch := filepath.Join(base, "scratch-1")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "projects"), filepath.Join(scratch, "projects")); err != nil {
		t.Fatal(err)
	}
	roots := []Root{{Harness: HarnessClaude, Profile: "personal", Dir: root, RetentionDays: 30}, {Harness: HarnessClaude, Dir: scratch}, {Harness: HarnessClaude, Dir: filepath.Join(base, "missing")}}
	var got []SourceRef
	if err := (Claude{}).Discover(context.Background(), roots, func(r SourceRef) error { got = append(got, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("discovered %d: %+v", len(got), got)
	}
	byNative := map[string]SourceRef{}
	for _, r := range got {
		byNative[r.NativeID] = r
	}
	top, ok := byNative["s1"]
	if !ok || top.IsSidechain || top.Profile != "personal" || top.RetentionDays != 30 || top.Ino == 0 || top.Size != 3 {
		t.Fatalf("top-level ref: %+v", top)
	}
	agent, ok := byNative["s1/agent-abc"]
	if !ok || !agent.IsSidechain || agent.ParentNativeID != "s1" {
		t.Fatalf("subagent ref: %+v", agent)
	}
}

func TestRecordType(t *testing.T) {
	if recordType([]byte(`{"parentUuid":null,"type":"user","message":{"type":"text"}}`)) != "user" {
		t.Fatal("type not found")
	}
	if recordType([]byte(`{"a":1}`)) != "" {
		t.Fatal("expected empty")
	}
	// Real attachment and progress records put a nested "type" before the
	// top-level one; the prefilter must read the top-level key.
	if got := recordType([]byte(`{"parentUuid":"u1","isSidechain":false,"attachment":{"type":"total_tokens_reminder","text":"x"},"type":"attachment","uuid":"at1"}`)); got != "attachment" {
		t.Fatalf("real attachment order: got %q", got)
	}
	if got := recordType([]byte(`{"data":{"type":"hook_progress","output":"\"type\":\"user\" }"},"type":"progress"}`)); got != "progress" {
		t.Fatalf("nested type inside a string: got %q", got)
	}
	if got := recordType([]byte(`{"message":{"content":[{"type":"text","text":"{\"type\":\"x"}]},"type":"assistant"}`)); got != "assistant" {
		t.Fatalf("type after an array: got %q", got)
	}
}

// Real files never put the top-level type first on attachment records;
// they must still be skipped before any decode and never counted unknown.
func TestClaude_PrefilterSkipsRealAttachmentRecords(t *testing.T) {
	real := `{"parentUuid":"u1","isSidechain":false,"attachment":{"type":"total_tokens_reminder","text":"zebra"},"type":"attachment","uuid":"at1","timestamp":"2026-09-05T08:20:56.699Z"}
{"parentUuid":"u1","isSidechain":false,"data":{"type":"hook_progress","output":"zebra"},"type":"progress","uuid":"p1"}
{"parentUuid":"u1","isSidechain":false,"type":"user","message":{"role":"user","content":"hello"},"uuid":"u1","timestamp":"2026-09-19T10:00:00.000Z","sessionId":"s1"}
`
	rec := newRecorder()
	if _, err := (Claude{}).Ingest(context.Background(), SourceRef{Path: writeTemp(t, real)}, 0, rec, nil); err != nil {
		t.Fatal(err)
	}
	if rec.counts[CountUnknownType] != 0 || len(rec.msgs) != 1 {
		t.Fatalf("unknown=%d msgs=%d: attachment/progress records were decoded", rec.counts[CountUnknownType], len(rec.msgs))
	}
}

func TestDigestArgs(t *testing.T) {
	d, touches := digestArgs("Write", []byte(`{"file_path":"/p/a.go","content":"`+strings.Repeat("x", 5000)+`"}`))
	if d != "/p/a.go" || len(touches) != 1 || touches[0].Op != "write" {
		t.Fatalf("%q %+v", d, touches)
	}
	d, touches = digestArgs("Grep", []byte(`{"pattern":"func main","path":"/p"}`))
	if d != "/p func main" || touches != nil {
		t.Fatalf("%q %+v", d, touches)
	}
	d, _ = digestArgs("Custom", []byte(`{"x":"`+strings.Repeat("é", 300)+`"}`))
	if len([]rune(d)) != ArgDigestChars {
		t.Fatalf("digest not clipped on runes: %d", len([]rune(d)))
	}
}

func TestBudget(t *testing.T) {
	var nilB *Budget
	if !nilB.Consume(10) || nilB.Exhausted() || nilB.Consumed() != 0 {
		t.Fatal("nil budget must be unlimited")
	}
	b := NewBudget(0, 0)
	if !b.Consume(1<<30) || b.Exhausted() || b.Consumed() != 1<<30 {
		t.Fatal("zero budget must be unlimited but count")
	}
	b = NewBudget(0, 100)
	if !b.Consume(50) || b.Consume(60) || !b.Exhausted() {
		t.Fatal("byte budget")
	}
	b = NewBudget(time.Nanosecond, 0)
	time.Sleep(time.Millisecond)
	if !b.Expired() || !b.Exhausted() {
		t.Fatal("deadline")
	}
}
