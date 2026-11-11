package query

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
)

func appendPiLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// piCorpus writes the shared realistic Pi session fixture (header with a
// parent session, model change, prompt, assistant thinking + text + two
// toolCalls, toolResults one errored, session_info, custom subagent
// message, compaction, a string prompt and an unknown record) and returns
// its path.
func piCorpus(t *testing.T) string {
	t.Helper()
	path, err := testcorpus.PiHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRowsPiMapping(t *testing.T) {
	if !SupportsDirectRows("pi") {
		t.Fatal("pi should be read directly")
	}
	rows, cursor := readRowsT(t, "pi", piCorpus(t), RowsOptions{})
	if cursor == "" {
		t.Fatal("no through cursor")
	}
	for _, kind := range []string{"user", "assistant", "thinking", "read", "bash", "system", "compaction", "other"} {
		if kindsOf(rows)[kind] == 0 {
			t.Errorf("no %s row", kind)
		}
	}
	// Dropped lifecycle records never become rows.
	for _, kind := range []string{"command", "tool", "edit", "subagent", "todo", "question", "skill", "turn_end"} {
		if kindsOf(rows)[kind] != 0 {
			t.Errorf("unexpected %s row", kind)
		}
	}
	user, ok := rowByID(rows, "m1")
	if !ok || user.Kind != "user" || user.Body != "Evaluate Hermes as a harness" {
		t.Errorf("user row: %+v", user)
	}
	// The assistant message's parts stay separate rows, keyed by part index.
	think, ok := rowByID(rows, "m2#0")
	if !ok || think.Kind != "thinking" || think.Title != "Thinking" || think.Body != "zebra thoughts" {
		t.Errorf("thinking row: %+v", think)
	}
	text, ok := rowByID(rows, "m2#1")
	if !ok || text.Kind != "assistant" || text.Body != "Reading the README first." {
		t.Errorf("assistant row: %+v", text)
	}
	// The toolCall row is unfinished until its toolResult merges into it.
	read, ok := rowByID(rows, "call_1")
	if !ok || read.Kind != "read" || read.ToolID != "call_1" || read.Detail != "/Users/x/README.md" || read.Meta["tool_name"] != "read" {
		t.Errorf("read tool row: %+v", read)
	}
	if read.Finished == nil || !*read.Finished || read.Body != "# README zebra result text" || read.IsError != nil && *read.IsError {
		t.Errorf("read result not merged: %+v", read)
	}
	if n, _ := read.Meta["lines"].(int); n != 1 {
		t.Errorf("read result line count: %+v", read.Meta)
	}
	bash, ok := rowByID(rows, "call_2")
	if !ok || bash.Kind != "bash" || bash.Detail != "ls notes" {
		t.Errorf("bash tool row: %+v", bash)
	}
	if bash.IsError == nil || !*bash.IsError || bash.Body != "ls: notes: No such file" {
		t.Errorf("error result not merged: %+v", bash)
	}
	model, ok := rowByID(rows, "b1d92e01")
	if !ok || model.Kind != "system" || model.Title != "Model changed to gpt-5.6-sol" {
		t.Errorf("model change row: %+v", model)
	}
	notice, ok := rowByID(rows, "c1")
	if !ok || notice.Kind != "system" || notice.Title != "subagent_supervisor_request" || notice.Body != "zebra supervisor notice" {
		t.Errorf("custom message row: %+v", notice)
	}
	compact, ok := rowByID(rows, "fe32007a")
	if !ok || compact.Kind != "compaction" || compact.Title != "Context compacted" || compact.Body != "## Goal\nEvaluate Hermes" {
		t.Errorf("compaction row: %+v", compact)
	}
	str, ok := rowByID(rows, "m6")
	if !ok || str.Kind != "user" || str.Body != "plain string prompt" {
		t.Errorf("string prompt row: %+v", str)
	}
	future, ok := rowByID(rows, "f1")
	if !ok || future.Kind != "other" || future.RawType != "future_record" {
		t.Errorf("unknown record row: %+v", future)
	}
}

func TestRowsPiTailAndBeforePaging(t *testing.T) {
	path := piCorpus(t)
	page, err := ReadRows(context.Background(), RowsSource{Harness: "pi", Path: path}, RowsOptions{Tail: 3})
	if err != nil {
		t.Fatal(err)
	}
	rows, cursor := page.Turns, page.BeforeCursor
	if len(rows) != 3 {
		t.Fatalf("tail rows: %+v", rows)
	}
	older, err := ReadRows(context.Background(), RowsSource{Harness: "pi", Path: path}, RowsOptions{Before: cursor, Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Turns) != 4 {
		t.Fatalf("older page rows: %+v", older.Turns)
	}
	if older.BeforeCursor == "" || older.ThroughCursor == "" {
		t.Fatalf("older page cursors: %+v", older)
	}
	seen := map[string]bool{}
	for _, r := range append(append([]Row{}, older.Turns...), rows...) {
		seen[r.ID] = true
	}
	for _, id := range []string{"m2#1", "call_1", "call_2", "c1", "fe32007a", "m6", "f1"} {
		if !seen[id] {
			t.Errorf("paged rows miss %s", id)
		}
	}
}

func TestRowsPiSinceUpdatesEarlierRows(t *testing.T) {
	path := piCorpus(t)
	_, cursor := readRowsT(t, "pi", path, RowsOptions{})
	tl, err := ReadRows(context.Background(), RowsSource{Harness: "pi", Path: path}, RowsOptions{Since: cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Turns) != 0 || len(tl.Updates) != 0 || len(tl.Removed) != 0 || tl.ThroughCursor != cursor {
		t.Fatalf("unchanged since read: %+v", tl)
	}
	// A toolResult arriving after the cursor is an update to the earlier
	// toolCall row, not a new row.
	line := `{"type":"message","id":"m7","parentId":"m6","timestamp":"2026-08-23T22:52:20.000Z","message":{"role":"toolResult","toolCallId":"call_2","toolName":"bash","content":[{"type":"text","text":"retry output"}],"isError":false}}` + "\n"
	appendPiLine(t, path, line)
	tl, err = ReadRows(context.Background(), RowsSource{Harness: "pi", Path: path}, RowsOptions{Since: cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Turns) != 0 || len(tl.Updates) != 1 || tl.Updates[0].ID != "call_2" || tl.Updates[0].Body != "retry output" {
		t.Fatalf("tool result update: %+v", tl)
	}
}

func TestRowsPiFindLanded(t *testing.T) {
	path := piCorpus(t)
	id, _, ok := FindLanded(context.Background(), "pi", path, 0, "Evaluate Hermes as a harness", time.Time{})
	if !ok || id != "m1" {
		t.Fatalf("landed id=%q ok=%v", id, ok)
	}
	if _, _, ok := FindLanded(context.Background(), "pi", path, 0, "never typed text", time.Time{}); ok {
		t.Fatal("unrelated text must not land")
	}
}
