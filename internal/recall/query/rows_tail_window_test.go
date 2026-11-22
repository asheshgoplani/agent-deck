package query

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestRowsKindsOpenCallOutsideTailWindow: an open tool call older than the
// 256 KiB tail window must still be listed in the filtered cursor's dropped
// ids, so its later result patch is not leaked on a since= page.
func TestRowsKindsOpenCallOutsideTailWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	call := `{"type":"assistant","uuid":"aold","message":{"id":"mold","role":"assistant","content":[{"type":"tool_use","id":"toolu_old","name":"Bash","input":{"command":"ls"}}]},"timestamp":"2026-11-18T10:00:00Z"}` + "\n"
	var b strings.Builder
	b.WriteString(call)
	pad := strings.Repeat("y", 1000)
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, `{"type":"user","uuid":"q-%d","message":{"role":"user","content":[{"type":"text","text":"%s %d"}]}}`+"\n", i, pad, i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	src := RowsSource{Harness: "claude", Path: path}
	opts := RowsOptions{Kinds: []string{"user"}, Tail: 1}
	snap, err := ReadRows(context.Background(), src, opts)
	if err != nil {
		t.Fatal(err)
	}
	c, err := decodeRowsCursor(snap.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.D, []string{"toolu_old"}) {
		t.Fatalf("dropped open ids = %v, want [toolu_old]", c.D)
	}
	reviewAppend(t, path, `{"type":"user","uuid":"rold","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_old","content":"BIG RESULT"}]},"timestamp":"2026-11-18T10:00:01Z"}`+"\n")
	next, err := ReadRows(context.Background(), src, RowsOptions{Kinds: []string{"user"}, Since: snap.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Updates) != 0 || len(next.Turns) != 0 {
		t.Fatalf("result leaked: turns=%v updates=%v", next.Turns, next.Updates)
	}
}
