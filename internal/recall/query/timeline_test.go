package query

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/ingest"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
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
// in the selected-turn projections used below.
func compactTurnJSON(t *testing.T, turns []Turn) string {
	t.Helper()
	type row struct {
		Role string `json:"role"`
		Kind string `json:"kind"`
		Tool string `json:"tool,omitempty"`
	}
	projection := make([]row, 0, len(turns))
	for _, turn := range turns {
		projection = append(projection, row{Role: turn.Role, Kind: turn.Kind, Tool: turn.ToolName})
	}
	b, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// canonicalTimelineJSON snapshots every client-visible turn field. Raw
// payloads are normalized through encoding/json so fixture object-key order
// cannot make a golden platform-dependent.
func canonicalTimelineJSON(t *testing.T, turns []Turn) []byte {
	t.Helper()
	copyTurns := append([]Turn(nil), turns...)
	for i := range copyTurns {
		if len(copyTurns[i].Raw) == 0 {
			continue
		}
		var value any
		if err := json.Unmarshal(copyTurns[i].Raw, &value); err != nil {
			t.Fatalf("turn %d has invalid raw JSON: %v", i, err)
		}
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		copyTurns[i].Raw = b
	}
	b, err := json.MarshalIndent(copyTurns, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func assertTimelineGolden(t *testing.T, harness string, turns []Turn) {
	t.Helper()
	got := canonicalTimelineJSON(t, turns)
	path := filepath.Join("testdata", "timeline", harness+".json")
	want, err := os.ReadFile(path)
	if err != nil {
		// The test-box runner ships committed HEAD but does not return files.
		// Print an exact snapshot that can be decoded into the checked-in
		// golden after this failing-first run.
		t.Logf("TIMELINE_GOLDEN_BASE64[%s]=%s", harness, base64.StdEncoding.EncodeToString(got))
		t.Errorf("read %s: %v", path, err)
		return
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s timeline differs from full canonical golden\ngot:\n%s\nwant:\n%s", harness, got, want)
	}
}

func TestTimelineHarnessGoldens(t *testing.T) {
	base := t.TempDir()
	claude := filepath.Join(base, "claude")
	writeSession(t, claude, sessA, "/Users/x/app", "", "Review the auth fix", "I will inspect it.")
	claudePath := filepath.Join(claude, "projects", "-p", sessA+".jsonl")
	f, err := os.OpenFile(claudePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./internal/auth"}}]},"uuid":"tool-call","timestamp":"2026-09-11T01:00:00Z","sessionId":%q}`+"\n", sessA))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"PASS"}]},"uuid":"tool-result","timestamp":"2026-09-11T01:00:01Z","sessionId":%q}`+"\n", sessA))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(base, "codex")
	pi := filepath.Join(base, "pi")
	gemini := filepath.Join(base, "gemini")
	opencodeBase := filepath.Join(base, "share")
	hermes := filepath.Join(base, "hermes")
	if _, err := testcorpus.CodexHome(codex, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := testcorpus.PiHome(pi); err != nil {
		t.Fatal(err)
	}
	if _, err := testcorpus.GeminiHome(gemini); err != nil {
		t.Fatal(err)
	}
	opencode, err := testcorpus.OpenCodeTree(opencodeBase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testcorpus.HermesHome(hermes); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(base, "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	roots := []reader.Root{
		{Harness: reader.HarnessClaude, Dir: claude},
		{Harness: reader.HarnessCodex, Dir: codex},
		{Harness: reader.HarnessPi, Dir: pi},
		{Harness: reader.HarnessGemini, Dir: gemini},
		{Harness: reader.HarnessOpenCode, Dir: opencode},
		{Harness: reader.HarnessHermes, Dir: hermes},
	}
	if _, err := ingest.New(st, ingest.Options{Roots: roots}).Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := New(st, "")
	// Each golden is the ordered, client-visible role/kind/tool projection of
	// the selected corpus records. Text needles make a missing record fail
	// before comparing JSON; unknown records must retain their raw payload.
	cases := []struct {
		harness string
		ref     string
		needles []string
		golden  string
	}{
		{"claude", sessA, []string{"Review the auth fix", "Bash", "PASS"}, `[{"role":"user","kind":"message"},{"role":"assistant","kind":"bash","tool":"Bash"},{"role":"user","kind":"tool_result"}]`},
		{"codex", testcorpus.CodexThread, []string{"Fix the flaky auth test", "shell", "FAIL", "Summary so far"}, `[{"role":"user","kind":"message"},{"role":"assistant","kind":"bash","tool":"shell"},{"role":"tool","kind":"tool_result"},{"role":"system","kind":"compaction"}]`},
		{"pi", testcorpus.PiID, []string{"Evaluate Hermes as a harness", "read", "README zebra result", "## Goal"}, `[{"role":"user","kind":"message"},{"role":"assistant","kind":"tool_call","tool":"read"},{"role":"tool","kind":"tool_result"},{"role":"system","kind":"compaction"}]`},
		{"gemini", "196a60d9-6cfb-4069-8443-a78a64cedff5", []string{"Fix the flaky auth test", "read_file", "go test ./internal/auth"}, `[{"role":"user","kind":"message"},{"role":"assistant","kind":"tool_call","tool":"read_file"},{"role":"assistant","kind":"bash","tool":"run_shell_command"}]`},
		{"opencode", testcorpus.OpenCodeSession, []string{"Fix the flaky auth test", "codesearch", "The root cause was clock skew"}, `[{"role":"user","kind":"message"},{"role":"assistant","kind":"tool_call","tool":"codesearch"},{"role":"assistant","kind":"message"}]`},
		{"hermes", testcorpus.HermesSession, []string{"hi, the clock skew", "terminal", "Error: no such dir"}, `[{"role":"user","kind":"message"},{"role":"assistant","kind":"bash","tool":"terminal"},{"role":"tool","kind":"tool_result","tool":"terminal"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.harness, func(t *testing.T) {
			timeline, err := s.Timeline(context.Background(), tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			assertTimelineGolden(t, tc.harness, timeline.Turns)
			if timeline.Session.Harness != tc.harness || timeline.ThroughCursor == "" {
				t.Fatalf("timeline identity/cursor = %+v", timeline)
			}
			var selected []Turn
			start := 0
			for _, needle := range tc.needles {
				found := false
				for i := start; i < len(timeline.Turns); i++ {
					turn := timeline.Turns[i]
					if strings.Contains(turn.Text, needle) || turn.ToolName == needle {
						selected = append(selected, turn)
						start = i + 1
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("missing %q after position %d: %+v", needle, start, timeline.Turns)
				}
			}
			if got := compactTurnJSON(t, selected); got != tc.golden {
				t.Errorf("timeline golden\ngot  %s\nwant %s", got, tc.golden)
			}
			for i, turn := range timeline.Turns {
				if i > 0 && turn.Seq <= timeline.Turns[i-1].Seq {
					t.Fatalf("source order lost at %d: %+v", i, timeline.Turns)
				}
				if turn.Kind == "other" && len(turn.Raw) == 0 {
					t.Errorf("unknown turn lost raw payload: %+v", turn)
				}
			}
		})
	}
}
