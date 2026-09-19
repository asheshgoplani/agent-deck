package recall

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQueue_EnqueueDrainNewestFirstDeduped(t *testing.T) {
	q := filepath.Join(t.TempDir(), "recall", "queue.jsonl")
	if got, err := Drain(q); err != nil || got != nil {
		t.Fatalf("empty drain: %v %v", got, err)
	}
	for i, p := range []string{"/a.jsonl", "/b.jsonl", "/a.jsonl"} {
		if err := Enqueue(q, QueueEntry{TS: int64(i + 1), Harness: "claude", Path: p, Event: "Stop"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Enqueue(q, QueueEntry{Path: ""}); err == nil {
		t.Fatal("empty path must be refused")
	}
	if n := QueueLen(q); n != 3 {
		t.Fatalf("len %d", n)
	}
	// A torn trailing line is skipped, not fatal.
	f, _ := os.OpenFile(q, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"ts":9,"path":"/c.js`)
	f.Close()
	got, err := Drain(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != "/a.jsonl" || got[0].TS != 3 || got[1].Path != "/b.jsonl" {
		t.Fatalf("drained %+v", got)
	}
	if _, err := os.Stat(q); !os.IsNotExist(err) {
		t.Fatal("queue must be gone after a drain")
	}
	if _, err := os.Stat(q + ".draining"); !os.IsNotExist(err) {
		t.Fatal("draining file must be removed")
	}
}
