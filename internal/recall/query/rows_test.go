package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var updateRowsGolden = flag.Bool("update-rows", false, "rewrite testdata/rows/*.golden.json")

func rowsFixture(t *testing.T, harness string) string {
	t.Helper()
	name := map[string]string{"claude": "claude-session.jsonl", "codex": "codex-rollout.jsonl"}[harness]
	return filepath.Join("testdata", "rows", name)
}

func readRowsT(t *testing.T, harness, path string, opts RowsOptions) ([]Row, string) {
	t.Helper()
	tl, err := ReadRows(context.Background(), RowsSource{Harness: harness, Path: path}, opts)
	if err != nil {
		t.Fatalf("ReadRows(%s): %v", path, err)
	}
	return tl.Turns, tl.ThroughCursor
}

func TestRowsGoldens(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			rows, cursor := readRowsT(t, harness, rowsFixture(t, harness), RowsOptions{})
			if cursor == "" {
				t.Fatal("no through cursor")
			}
			got, err := json.MarshalIndent(rows, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "rows", harness+".golden.json")
			if *updateRowsGolden {
				if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(got)) {
				t.Fatalf("rows differ from %s (run with -update-rows after reviewing):\n%s", golden, got)
			}
		})
	}
}

func rowByID(rows []Row, id string) (Row, bool) {
	for _, r := range rows {
		if r.ID == id {
			return r, true
		}
	}
	return Row{}, false
}

func kindsOf(rows []Row) map[string]int {
	out := map[string]int{}
	for _, r := range rows {
		out[r.Kind]++
	}
	return out
}

func TestRowsClaudeMapping(t *testing.T) {
	rows, _ := readRowsT(t, "claude", rowsFixture(t, "claude"), RowsOptions{})
	for _, kind := range []string{"user", "assistant", "thinking", "bash", "edit", "read", "subagent", "todo", "question", "skill", "tool", "command", "system", "compaction", "turn_end", "other"} {
		if kindsOf(rows)[kind] == 0 {
			t.Errorf("no %s row", kind)
		}
	}
	bash, _ := rowByID(rows, "toolu_bash1")
	if bash.Kind != "bash" || bash.Title != "Run hello.py" || bash.ToolID != "toolu_bash1" || bash.Detail != "python3 hello.py\nls" || bash.Body != "1\n2\n3\n4" || bash.Finished == nil || !*bash.Finished || bash.Summary != "… +1 lines" {
		t.Errorf("bash row: %+v", bash)
	}
	edit, _ := rowByID(rows, "toolu_edit1")
	if edit.Meta["added"] != 2 || edit.Meta["removed"] != 1 || edit.Summary != "Added 2 lines, removed 1 line" {
		t.Errorf("edit row +/-: %+v", edit)
	}
	read, _ := rowByID(rows, "toolu_read1")
	if read.IsError == nil || !*read.IsError {
		t.Errorf("error result not merged: %+v", read)
	}
	q, _ := rowByID(rows, "toolu_q1")
	if q.Title != "Which?" || q.Summary != "Which? → A" {
		t.Errorf("question: %+v", q)
	}
	todo, _ := rowByID(rows, "toolu_todo1")
	if todo.Summary != "1 of 2 done" {
		t.Errorf("todo: %+v", todo)
	}
	think, _ := rowByID(rows, "as1")
	if think.Kind != "thinking" {
		t.Errorf("single-block row id should be the uuid: %+v", think)
	}
	multi := newRowParser("claude", rowParserState{}).line([]byte(`{"type":"assistant","uuid":"m9","timestamp":"t","message":{"content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"a"},{"type":"tool_use","id":"toolu_z","name":"Bash","input":{"command":"ls"}}]}}`))
	if len(multi) != 3 || multi[0].Row.ID != "m9#0" || multi[1].Row.ID != "m9#1" || multi[2].Row.ID != "toolu_z" {
		t.Errorf("multi-block ids: %+v %+v %+v", multi[0].Row, multi[1].Row, multi[2].Row)
	}
	// Sidechain rows are children of their subagent row.
	agent, _ := rowByID(rows, "toolu_agent1")
	if len(agent.Children) != 3 || agent.Children[1].Kind != "bash" || !strings.HasPrefix(agent.Children[0].ID, "sub:agent42:") || agent.Summary != "Done (1 tool use)" {
		t.Errorf("subagent children: %+v", agent)
	}
	compact, _ := rowByID(rows, "s4")
	if compact.Meta["pre_tokens"] != int64(844000) || compact.Meta["post_tokens"] != int64(19000) {
		t.Errorf("compaction meta: %+v", compact.Meta)
	}
	turn, _ := rowByID(rows, "s3")
	if turn.Meta["duration_ms"] != int64(75000) {
		t.Errorf("turn_end meta: %+v", turn.Meta)
	}
	// Mid-turn message: no user row exists, the queue row stays, at the
	// remove timestamp, and the queued_command attachment is not repeated.
	var absorbed []Row
	for _, r := range rows {
		if strings.Contains(r.Body, "MIDTURN") {
			absorbed = append(absorbed, r)
		}
	}
	if len(absorbed) != 1 || absorbed[0].Kind != "user" || absorbed[0].Queued == nil || *absorbed[0].Queued || absorbed[0].TS != "2026-09-23T08:00:17.000Z" {
		t.Fatalf("absorbed mid-turn message: %+v", absorbed)
	}
	// A dequeued message is replaced by its user row; a removed one is gone.
	for _, r := range rows {
		if r.Body == "next task please" && r.Queued != nil {
			t.Errorf("dequeued placeholder kept: %+v", r)
		}
		if r.Body == "oops typo" {
			t.Errorf("removed queue entry kept: %+v", r)
		}
		if r.Kind == "system" && r.Title == "" {
			t.Errorf("system row without title: %+v", r)
		}
	}
	// Pure model context and bookkeeping rows are dropped, never "other".
	for _, r := range rows {
		if r.Kind == "other" && r.Title != "brand-new-row" {
			t.Errorf("unexpected other row: %+v", r)
		}
	}
}

func TestRowsCodexMapping(t *testing.T) {
	rows, _ := readRowsT(t, "codex", rowsFixture(t, "codex"), RowsOptions{})
	for _, r := range rows {
		if strings.Contains(r.Body, "AGENTS.md") || strings.Contains(r.Body, "environment_context") || strings.Contains(r.Body, "dev instructions") {
			t.Errorf("boilerplate leaked: %+v", r)
		}
		if r.ID == "call_exec1" {
			t.Errorf("code-mode exec wrapper duplicated its CommandExecution: %+v", r)
		}
	}
	if n := kindsOf(rows)["user"]; n != 1 {
		t.Errorf("user rows = %d, want 1 (item_completed UserMessage is a duplicate)", n)
	}
	if n := kindsOf(rows)["assistant"]; n != 1 {
		t.Errorf("assistant rows = %d, want 1", n)
	}
	read, _ := rowByID(rows, "exec-2")
	if read.Kind != "read" || read.Summary != "Read 1 line" {
		t.Errorf("read-only command: %+v", read)
	}
	failed, _ := rowByID(rows, "exec-3")
	if failed.Meta["exit_code"] != 1 || failed.IsError == nil || !*failed.IsError {
		t.Errorf("failed command: %+v", failed)
	}
	sub, _ := rowByID(rows, "call_spawn")
	if sub.Kind != "subagent" || sub.Meta["agent_id"] != "th2" {
		t.Errorf("spawn_agent row: %+v", sub)
	}
	edit, _ := rowByID(rows, "fc-1")
	if edit.Summary != "Added 1 line, removed 1 line" {
		t.Errorf("FileChange: %+v", edit)
	}
	turn, _ := rowByID(rows, "turn:turn1")
	tokens, _ := turn.Meta["tokens"].(map[string]any)
	if turn.Kind != "turn_end" || turn.Meta["duration_ms"] != int64(56000) || tokens["total"] != 25165 {
		t.Errorf("turn_end: %+v", turn)
	}
	aborted, _ := rowByID(rows, "turn:turn2")
	if aborted.Meta["status"] != "interrupted" {
		t.Errorf("turn_aborted: %+v", aborted)
	}
	model := false
	for _, r := range rows {
		if r.Title == "Model changed to gpt-6-mini" {
			model = true
		}
	}
	if !model {
		t.Error("no model-change system row")
	}
}

func TestRowsIDsStableAcrossTailWindow(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		full, _ := readRowsT(t, harness, rowsFixture(t, harness), RowsOptions{})
		info, err := os.Stat(rowsFixture(t, harness))
		if err != nil {
			t.Fatal(err)
		}
		_ = info
		tail, cursor := readRowsT(t, harness, rowsFixture(t, harness), RowsOptions{Tail: 5})
		if len(tail) != 5 || cursor == "" {
			t.Fatalf("%s tail: %d rows", harness, len(tail))
		}
		for i, r := range tail {
			want := full[len(full)-5+i]
			if r.ID != want.ID || r.Kind != want.Kind {
				t.Errorf("%s: tail row %d = %s/%s, want %s/%s", harness, i, r.ID, r.Kind, want.ID, want.Kind)
			}
		}
		_, fullCursor := readRowsT(t, harness, rowsFixture(t, harness), RowsOptions{})
		a, _ := decodeRowsCursor(cursor)
		b, _ := decodeRowsCursor(fullCursor)
		if a.O != b.O || a.A != b.A {
			t.Errorf("%s: tail cursor offset %d, full %d", harness, a.O, b.O)
		}
	}
}

// followCollector applies follow frames the way a client does.
type followCollector struct {
	mu      sync.Mutex
	set     *rowSet
	frames  []RowFrame
	cursors []string
	status  []*LiveStatus
}

func (c *followCollector) emit(f RowFrame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = append(c.frames, f)
	if f.Cursor != "" {
		c.cursors = append(c.cursors, f.Cursor)
	}
	if f.LiveStatus != nil {
		c.status = append(c.status, f.LiveStatus)
	}
	c.set.apply(f)
	return nil
}

func (c *followCollector) snapshot() ([]Row, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.set.list(), append([]string(nil), c.cursors...)
}

func startFollow(t *testing.T, src RowsSource, after string, status func() *LiveStatus, c *followCollector) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- FollowRows(ctx, src, after, 20*time.Millisecond, status, c.emit) }()
	return func() error {
		cancel()
		err := <-done
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fixtureLines(t *testing.T, harness string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(rowsFixture(t, harness))
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for _, l := range bytes.SplitAfter(data, []byte("\n")) {
		if len(l) > 0 {
			out = append(out, l)
		}
	}
	return out
}

func appendFile(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// TestRowsFollowMatchesTimelineAndResumes appends a transcript line by line
// (with torn writes), follows it, kills the follower half way and resumes
// from its last cursor: the client ends with exactly the timeline's rows,
// nothing lost and nothing duplicated.
func TestRowsFollowMatchesTimelineAndResumes(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "t.jsonl")
			if harness == "claude" {
				// The sidechain lives next to the transcript.
				sub := filepath.Join(dir, "t", "subagents")
				if err := os.MkdirAll(sub, 0o755); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join("testdata", "rows", "claude-session", "subagents", "agent-agent42.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(sub, "agent-agent42.jsonl"), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			lines := fixtureLines(t, harness)
			appendFile(t, path, lines[0])
			_, cursor := readRowsT(t, harness, path, RowsOptions{})
			src := RowsSource{Harness: harness, Path: path}
			c := &followCollector{set: newRowSet()}
			first, _ := readRowsT(t, harness, path, RowsOptions{})
			for _, r := range first {
				c.set.apply(RowFrame{Frame: "row", Row: &r})
			}
			stop := startFollow(t, src, cursor, nil, c)
			half := len(lines) / 2
			for _, l := range lines[1:half] {
				// A torn write: the first half lands, then the rest.
				appendFile(t, path, l[:len(l)/2])
				time.Sleep(3 * time.Millisecond)
				appendFile(t, path, l[len(l)/2:])
			}
			want, _ := readRowsT(t, harness, path, RowsOptions{})
			waitFor(t, "first half", func() bool { got, _ := c.snapshot(); return len(got) == len(want) })
			if err := stop(); err != nil {
				t.Fatal(err)
			}
			_, cursors := c.snapshot()
			if len(cursors) == 0 {
				t.Fatal("follow emitted no cursor")
			}
			// Resume from the last cursor while the rest is appended.
			for _, l := range lines[half:] {
				appendFile(t, path, l)
			}
			stop = startFollow(t, src, cursors[len(cursors)-1], nil, c)
			want, _ = readRowsT(t, harness, path, RowsOptions{})
			waitFor(t, "all rows", func() bool { got, _ := c.snapshot(); return len(got) >= len(want) })
			time.Sleep(100 * time.Millisecond)
			if err := stop(); err != nil {
				t.Fatal(err)
			}
			got, _ := c.snapshot()
			if !reflect.DeepEqual(normalizeRows(t, got), normalizeRows(t, want)) {
				gb, _ := json.MarshalIndent(got, "", " ")
				wb, _ := json.MarshalIndent(want, "", " ")
				t.Fatalf("follow != timeline\nfollow:\n%s\ntimeline:\n%s", gb, wb)
			}
			for _, f := range c.frames {
				if f.Frame == "resync_required" {
					t.Fatalf("unexpected resync: %+v", f)
				}
			}
		})
	}
}

// normalizeRows round-trips rows through JSON so a streamed row and a
// timeline row compare by their wire form (meta number types differ in Go).
func normalizeRows(t *testing.T, rows []Row) string {
	t.Helper()
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRowsFollowEmitsSidechainChildren(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	sub := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join("testdata", "rows", "claude-session", "subagents", "agent-agent42.jsonl"))
	if err := os.WriteFile(filepath.Join(sub, "agent-agent42.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	lines := fixtureLines(t, "claude")
	var agentLine, resultLine []byte
	for _, l := range lines {
		if bytes.Contains(l, []byte(`"toolu_agent1"`)) && bytes.Contains(l, []byte(`"name": "Agent"`)) {
			agentLine = l
		}
		if bytes.Contains(l, []byte(`"agentId": "agent42"`)) || bytes.Contains(l, []byte(`"agentId":"agent42"`)) {
			resultLine = l
		}
	}
	if agentLine == nil || resultLine == nil {
		t.Fatal("fixture lines missing")
	}
	appendFile(t, path, agentLine)
	_, cursor := readRowsT(t, "claude", path, RowsOptions{})
	c := &followCollector{set: newRowSet()}
	stop := startFollow(t, RowsSource{Harness: "claude", Path: path}, cursor, nil, c)
	appendFile(t, path, resultLine)
	waitFor(t, "children", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, f := range c.frames {
			if f.Frame == "update" && f.Row.ID == "toolu_agent1" && len(f.Row.Children) == 3 {
				return true
			}
		}
		return false
	})
	_ = stop()
}

func TestRowsFollowResync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.jsonl")
	lines := fixtureLines(t, "claude")
	appendFile(t, path, bytes.Join(lines[:6], nil))
	_, cursor := readRowsT(t, "claude", path, RowsOptions{})
	run := func(src RowsSource, after string) RowFrame {
		var got RowFrame
		err := FollowRows(context.Background(), src, after, 10*time.Millisecond, nil, func(f RowFrame) error {
			got = f
			if f.Frame == "resync_required" {
				return nil
			}
			return errors.New("unexpected frame " + f.Frame)
		})
		if err != nil {
			t.Fatalf("follow: %v", err)
		}
		return got
	}
	src := RowsSource{Harness: "claude", Path: path}
	if f := run(src, "not-a-cursor"); f.Reason != "invalid_cursor" {
		t.Errorf("invalid cursor: %+v", f)
	}
	// Rewritten history of the same length: the anchor no longer matches.
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, bytes.ReplaceAll(data, []byte("plan it"), []byte("PLAN IT")), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := run(src, cursor); f.Reason != "source_rewritten" {
		t.Errorf("rewrite: %+v", f)
	}
	if err := os.WriteFile(path, lines[0], 0o644); err != nil {
		t.Fatal(err)
	}
	if f := run(src, cursor); f.Reason != "source_shortened" {
		t.Errorf("shortened: %+v", f)
	}
	other := filepath.Join(dir, "other.jsonl")
	appendFile(t, other, lines[0])
	if f := run(RowsSource{Harness: "claude", Path: other}, cursor); f.Reason != "source_moved" {
		t.Errorf("moved: %+v", f)
	}
}

func TestRowsFollowSourceMovedByResolver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-a.jsonl")
	appendFile(t, path, fixtureLines(t, "codex")[0])
	_, cursor := readRowsT(t, "codex", path, RowsOptions{})
	moved := filepath.Join(dir, "rollout-b.jsonl")
	src := RowsSource{Harness: "codex", Path: path, Resolve: func() (string, error) { return moved, nil }}
	var got RowFrame
	err := FollowRows(context.Background(), src, cursor, 10*time.Millisecond, nil, func(f RowFrame) error { got = f; return nil })
	if err != nil || got.Frame != "resync_required" || got.Reason != "source_moved" {
		t.Fatalf("resolver move: %v %+v", err, got)
	}
}

// TestRowsFollowStatusFrames: status frames go out once a second while the
// session runs, once when it stops, and never again while it stays idle.
func TestRowsFollowStatusFrames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "st.jsonl")
	appendFile(t, path, fixtureLines(t, "claude")[0])
	_, cursor := readRowsT(t, "claude", path, RowsOptions{})
	var mu sync.Mutex
	state := &LiveStatus{SessionID: "s1", Running: true, SessionStatus: "running", Verb: "Cogitating…", ElapsedS: 3}
	status := func() *LiveStatus {
		mu.Lock()
		defer mu.Unlock()
		s := *state
		return &s
	}
	c := &followCollector{set: newRowSet()}
	stop := startFollow(t, RowsSource{Harness: "claude", Path: path}, cursor, status, c)
	waitFor(t, "two running frames", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.status) >= 2 })
	mu.Lock()
	state = &LiveStatus{SessionID: "s1", SessionStatus: "waiting"}
	mu.Unlock()
	waitFor(t, "stopped frame", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.status[len(c.status)-1].Running
	})
	c.mu.Lock()
	n := len(c.status)
	c.mu.Unlock()
	time.Sleep(2200 * time.Millisecond) // idle: nothing more
	_ = stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.status) != n {
		t.Fatalf("idle status repeated: %d then %d frames", n, len(c.status))
	}
	for _, s := range c.status[:n-1] {
		if !s.Running || s.Verb != "Cogitating…" || s.SessionID != "s1" {
			t.Fatalf("running frame: %+v", s)
		}
	}
	line, _ := json.Marshal(c.frames[len(c.frames)-1])
	if !strings.Contains(string(line), `"frame":"status"`) || !strings.Contains(string(line), `"running":false`) || !strings.Contains(string(line), `"session_id":"s1"`) {
		t.Fatalf("status wire shape: %s", line)
	}
}

// TestRowsSinceAndLimit pages a transcript forward with --limit and --since
// and ends with exactly the full timeline; changes to rows of an earlier
// page arrive as updates/removed.
func TestRowsSinceAndLimit(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		full, _ := readRowsT(t, harness, rowsFixture(t, harness), RowsOptions{})
		set := newRowSet()
		cursor := ""
		for page := 0; page < 100; page++ {
			tl, err := ReadRows(context.Background(), RowsSource{Harness: harness, Path: rowsFixture(t, harness)}, RowsOptions{Since: cursor, Limit: 4})
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range tl.Updates {
				u := u
				set.apply(RowFrame{Frame: "update", Row: &u})
			}
			for _, id := range tl.Removed {
				set.apply(RowFrame{Frame: "remove", ID: id})
			}
			for _, r := range tl.Turns {
				r := r
				set.apply(RowFrame{Frame: "row", Row: &r})
			}
			if tl.ThroughCursor == cursor {
				break
			}
			cursor = tl.ThroughCursor
		}
		if got, want := normalizeRows(t, set.list()), normalizeRows(t, full); got != want {
			t.Fatalf("%s: paged rows differ\npaged: %s\nfull:  %s", harness, got, want)
		}
	}
	if _, err := ReadRows(context.Background(), RowsSource{Harness: "claude", Path: rowsFixture(t, "claude")}, RowsOptions{Since: "garbage"}); err == nil {
		t.Fatal("invalid --since accepted")
	}
}

func TestRowsUnsupportedHarness(t *testing.T) {
	_, err := ReadRows(context.Background(), RowsSource{Harness: "gemini", Path: "x"}, RowsOptions{})
	if !errors.Is(err, ErrRowsUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestRowsFromTurnsCoversV1Kinds(t *testing.T) {
	rows := rowsFromTurns([]Turn{
		{Role: "user", Kind: "message", Text: "hi"},
		{Role: "assistant", Kind: "message", Text: "yo"},
		{Role: "assistant", Kind: "bash", ToolName: "bash", Text: "ls"},
		{Role: "tool", Kind: "tool_result", Text: "a\nb"},
		{Role: "system", Kind: "system"},
		{Role: "system", Kind: "other", Raw: json.RawMessage(`{"x":1}`)},
	})
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, r.Kind)
	}
	if strings.Join(kinds, ",") != "user,assistant,bash,system,other" {
		t.Fatalf("kinds = %v", kinds)
	}
}
