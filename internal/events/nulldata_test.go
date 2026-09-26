package events

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// TestNilDataIsOmitted: a producer passing nil (tmux.output) must not put
// `"data":null` on the wire; the frame simply has no data.
func TestNilDataIsOmitted(t *testing.T) {
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.Publish("tmux.output", "agentdeck_x", nil)
	b.Publish("session.status", "s1", map[string]string{"to": "running"})
	if !b.Flush(2 * time.Second) {
		t.Fatal("flush")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, _ := b.Subscribe(ctx, 0)
	var lines [][]byte
	for f := range sub.Frames() {
		line, err := f.CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
		if len(lines) == 2 {
			cancel()
		}
	}
	if len(lines) != 2 {
		t.Fatalf("frames: %d", len(lines))
	}
	if bytes.Contains(lines[0], []byte(`"data"`)) {
		t.Fatalf("nil data serialized: %s", lines[0])
	}
	if !bytes.Contains(lines[1], []byte(`"data":{"to":"running"}`)) {
		t.Fatalf("data lost: %s", lines[1])
	}
	counts, err := b.KindCounts()
	if err != nil || counts["tmux.output"] != 1 || counts["session.status"] != 1 {
		t.Fatalf("kind counts: %v %v", counts, err)
	}
	// A frame already stored with data:null (older writer) still renders
	// without it.
	f := Frame{Cursor: 1, Kind: "tmux.output", Data: []byte("null")}
	line, _ := f.CanonicalJSON()
	if bytes.Contains(line, []byte("data")) {
		t.Fatalf("stored null rendered: %s", line)
	}
}
