package query

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func appendClaudeTurn(t *testing.T, path, uuid, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	line := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q},"uuid":%q,"timestamp":"2026-09-11T00:00:00Z","sessionId":%q}`+"\n", text, uuid, sessA)
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

func TestTimelineCursorAndFollowResume(t *testing.T) {
	f := newFixture(t)
	f.sweep(t)
	s := New(f.st, f.stateDB)
	ctx := context.Background()
	before, err := s.Timeline(ctx, sessA)
	if err != nil {
		t.Fatal(err)
	}
	if before.Session.NativeID != sessA || len(before.Turns) == 0 || before.ThroughCursor == "" {
		t.Fatalf("incomplete timeline: %+v", before)
	}
	for i := 1; i < len(before.Turns); i++ {
		if before.Turns[i-1].Seq >= before.Turns[i].Seq {
			t.Fatalf("timeline not ordered: %+v", before.Turns)
		}
	}
	path := writeSessionPathForTimeline(t, f)
	appendClaudeTurn(t, path, "new-turn-1", "timeline follow one")
	appendClaudeTurn(t, path, "new-turn-2", "timeline follow two")
	f.sweep(t)
	after, err := s.Timeline(ctx, sessA)
	if err != nil {
		t.Fatal(err)
	}
	var frames []Frame
	followCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err = s.Follow(followCtx, sessA, before.ThroughCursor, func(frame Frame) error {
		frames = append(frames, frame)
		if len(frames) == len(after.Turns)-len(before.Turns) {
			cancel()
		}
		return nil
	})
	if err != nil && err != context.Canceled {
		t.Fatal(err)
	}
	if got, want := len(frames), len(after.Turns)-len(before.Turns); got != want || got != 2 {
		t.Fatalf("resume frames=%d, new timeline turns=%d: %+v", got, want, frames)
	}
	for i, frame := range frames {
		want := after.Turns[len(before.Turns)+i]
		if frame.Type != "turn" || frame.Turn == nil || frame.Turn.Seq != want.Seq || frame.Turn.Text != want.Text || frame.Cursor == "" {
			t.Fatalf("frame %d=%+v, want turn %+v", i, frame, want)
		}
	}
}

// newFixture puts sessA in the first Claude root. Resolve supplies its
// physical path, keeping this test independent of the fixture's layout.
func writeSessionPathForTimeline(t *testing.T, f *fixture) string {
	t.Helper()
	row, err := New(f.st, f.stateDB).Resolve(context.Background(), sessA)
	if err != nil {
		t.Fatal(err)
	}
	if row.Path == "" {
		t.Fatal("fixture session has no source path")
	}
	return row.Path
}

func TestFollowStaleCursorRequiresResync(t *testing.T) {
	f := newFixture(t)
	f.sweep(t)
	s := New(f.st, f.stateDB)
	before, err := s.Timeline(context.Background(), sessA)
	if err != nil {
		t.Fatal(err)
	}
	path := writeSessionPathForTimeline(t, f)
	// A source rewrite invalidates an offset based cursor even when the
	// session identity survives. A new timeline is the restart point.
	replacement := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"replacement after rewrite"},"uuid":"replacement-1","timestamp":"2026-09-11T01:00:00Z","sessionId":%q}`+"\n", sessA)
	if err := os.WriteFile(path, []byte(replacement), 0o644); err != nil {
		t.Fatal(err)
	}
	f.sweep(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var frames []Frame
	err = s.Follow(ctx, sessA, before.ThroughCursor, func(frame Frame) error {
		frames = append(frames, frame)
		cancel()
		return nil
	})
	if err != nil && err != context.Canceled {
		t.Fatal(err)
	}
	if len(frames) != 1 || frames[0].Type != "resync_required" || frames[0].Turn != nil {
		t.Fatalf("stale follow frames = %+v", frames)
	}
	fresh, err := s.Timeline(context.Background(), sessA)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ThroughCursor == "" || fresh.ThroughCursor == before.ThroughCursor || len(fresh.Turns) != 1 || !strings.Contains(fresh.Turns[0].Text, "replacement after rewrite") {
		t.Fatalf("fresh timeline after resync = %+v", fresh)
	}
}

func TestFollowSeesSourceAppendWithinTwoSeconds(t *testing.T) {
	f := newFixture(t)
	f.sweep(t)
	s := New(f.st, f.stateDB)
	before, err := s.Timeline(context.Background(), sessA)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	frames := make(chan Frame, 1)
	done := make(chan error, 1)
	go func() {
		done <- s.Follow(ctx, sessA, before.ThroughCursor, func(frame Frame) error {
			select {
			case frames <- frame:
			case <-ctx.Done():
			}
			return nil
		})
	}()
	// Give the follower time to establish its subscription before appending.
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	appendClaudeTurn(t, writeSessionPathForTimeline(t, f), "latency-turn", "live follow latency")
	select {
	case frame := <-frames:
		if elapsed := time.Since(start); elapsed >= 2*time.Second {
			t.Errorf("follow append latency %v exceeds 2s", elapsed)
		}
		if frame.Type != "turn" || frame.Turn == nil || !strings.Contains(frame.Turn.Text, "live follow latency") {
			t.Errorf("follow append frame = %+v", frame)
		}
	case <-time.After(2 * time.Second):
		t.Error("follow did not emit appended turn within 2s")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			t.Errorf("follow shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("follow did not stop after cancellation")
	}
}

// A wire shape assertion catches accidental map-order or omitted-key drift
// in the compact client-facing projection used by the corpus goldens below.
func compactTurnJSON(t *testing.T, turns []Turn) string {
	t.Helper()
	type row struct {
		Role string `json:"role"`
		Kind string `json:"kind"`
		Tool string `json:"tool,omitempty"`
		Text string `json:"text,omitempty"`
	}
	projection := make([]row, 0, len(turns))
	for _, turn := range turns {
		projection = append(projection, row{Role: turn.Role, Kind: turn.Kind, Tool: turn.ToolName, Text: turn.Text})
	}
	b, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
