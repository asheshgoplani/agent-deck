package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// PR-B0 pins. The maintainer's ask: "Keep the CLI's behaviour and messages
// byte-identical, with tests on both paths: the CLI send and the daemon wake
// both refuse and both publish session.budget_exceeded."
//
// Golden files live in testdata/send_budget_refusal/. They were captured
// from the built binary on main 248ec999, before the gate moved into
// internal/session, with:
//
//	UPDATE_GOLDEN=1 go test ./cmd/agent-deck/ -run TestSessionSend_BudgetRefusalIsByteIdentical
//
// Only two values vary between runs and are normalized (after being checked
// to be well formed): the session id and resets_at (the next day start).
// Everything else is compared byte for byte (the refusal sentence, the
// --json key order and indentation, the exit code), except the bus frame,
// which is compared in canonical form: its JSON re-encoded with sorted keys.

const budgetGoldenDir = "testdata/send_budget_refusal"

var resetsAtRE = regexp.MustCompile(`"resets_at": "([^"]*)"`)

// budgetBusFrames is every session.budget_exceeded frame on the profile's
// events bus under home, in order.
func budgetBusFrames(t *testing.T, home string) []map[string]any {
	t.Helper()
	var frames []map[string]any
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".ndjson") || !strings.Contains(p, "bus") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, "session.budget_exceeded") {
				continue
			}
			var f map[string]any
			if err := json.Unmarshal([]byte(line), &f); err != nil {
				t.Fatalf("bus frame %q: %v", line, err)
			}
			if f["kind"] == "session.budget_exceeded" {
				frames = append(frames, f)
			}
		}
		return nil
	})
	return frames
}

var eventIDRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

// budgetFrameGolden is the one frame, minus what differs per run, as indented
// JSON. Before they are dropped, cursor must be a positive number, event_id 16
// hex digits, and ts (epoch ms) inside [before, after], the window the run
// happened in; session_id is replaced.
func budgetFrameGolden(t *testing.T, f map[string]any, id string, before, after time.Time) string {
	t.Helper()
	if c, ok := f["cursor"].(float64); !ok || c <= 0 {
		t.Fatalf("frame cursor = %v, want > 0", f["cursor"])
	}
	if e, ok := f["event_id"].(string); !ok || !eventIDRE.MatchString(e) {
		t.Fatalf("frame event_id = %v, want 16 hex digits", f["event_id"])
	}
	if ts, ok := f["ts"].(float64); !ok || int64(ts) < before.UnixMilli() || int64(ts) > after.UnixMilli() {
		t.Fatalf("frame ts = %v, want within [%d, %d] ms", f["ts"], before.UnixMilli(), after.UnixMilli())
	}
	for _, k := range []string{"cursor", "event_id", "ts"} {
		delete(f, k)
	}
	if f["session_id"] != id {
		t.Fatalf("frame session_id = %v, want %s", f["session_id"], id)
	}
	f["session_id"] = "<SESSION_ID>"
	var out strings.Builder
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// nextLocalMidnight is the start of the day after t in the budget timezone
// (the config default, "Local").
func nextLocalMidnight(t time.Time) time.Time {
	t = t.In(time.Local)
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.Local)
}

// normalizeBudgetJSON checks resets_at is exactly the next local midnight
// (of the day the run started or ended in, so a run across midnight passes)
// and replaces it and the session id with placeholders.
func normalizeBudgetJSON(t *testing.T, stdout, id string, before, after time.Time) string {
	t.Helper()
	m := resetsAtRE.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("no resets_at in %s", stdout)
	}
	at, err := time.Parse(time.RFC3339, m[1])
	if err != nil || !(at.Equal(nextLocalMidnight(before)) || at.Equal(nextLocalMidnight(after))) {
		t.Fatalf("resets_at = %q, want %s (next local midnight)", m[1], nextLocalMidnight(before).Format(time.RFC3339))
	}
	out := resetsAtRE.ReplaceAllString(stdout, `"resets_at": "<RESETS_AT>"`)
	return strings.ReplaceAll(out, id, "<SESSION_ID>")
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join(budgetGoldenDir, name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(budgetGoldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (capture with UPDATE_GOLDEN=1 on main)", err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from the golden.\n got: %q\nwant: %q", name, got, string(want))
	}
}

// TestSessionSend_BudgetRefusalIsByteIdentical is a CHARACTERIZATION test:
// it records what `session send` does today when the target's enforced
// budget is spent, and must pass unchanged before and after the gate moves
// into internal/session. A diff here means the move changed the CLI's
// behaviour, which PR-B0 promises it does not. It is not a spec of what the
// refusal should say.
//
// It pins, per mode, the refusal on stderr (human) or stdout (--json), the
// exit code, and that exactly one session.budget_exceeded frame reached the
// bus, with its data.
func TestSessionSend_BudgetRefusalIsByteIdentical(t *testing.T) {
	t.Run("human", func(t *testing.T) {
		home, id := budgetHome(t, enforcedSpentBudget, 5)
		before := time.Now()
		stdout, stderr, code := runAgentDeck(t, home, "session", "send", id, "wake up")
		after := time.Now()
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		checkGolden(t, "human_stderr.golden", strings.ReplaceAll(stderr, id, "<SESSION_ID>"))
		checkGolden(t, "human_exit_code.golden", strconv.Itoa(code)+"\n")
		frames := budgetBusFrames(t, home)
		if len(frames) != 1 {
			t.Fatalf("%d session.budget_exceeded frames, want exactly 1: %v", len(frames), frames)
		}
		checkGolden(t, "human_event.golden", budgetFrameGolden(t, frames[0], id, before, after))
	})
	t.Run("json", func(t *testing.T) {
		home, id := budgetHome(t, enforcedSpentBudget, 5)
		before := time.Now()
		stdout, stderr, code := runAgentDeck(t, home, "session", "send", id, "wake up", "--json")
		after := time.Now()
		if stderr != "" {
			t.Fatalf("stderr = %q, want empty", stderr)
		}
		checkGolden(t, "json_stdout.golden", normalizeBudgetJSON(t, stdout, id, before, after))
		checkGolden(t, "json_exit_code.golden", strconv.Itoa(code)+"\n")
		frames := budgetBusFrames(t, home)
		if len(frames) != 1 {
			t.Fatalf("%d session.budget_exceeded frames, want exactly 1: %v", len(frames), frames)
		}
		checkGolden(t, "json_event.golden", budgetFrameGolden(t, frames[0], id, before, after))
	})
}

// TestBudgetGate_CLISendRefusesAndPublishes is the CLI half of the
// maintainer's R5: `session send` to a spent target is refused, types
// nothing, and publishes one session.budget_exceeded. The daemon half is
// TestBudgetGate_DaemonWakeRefusesAndPublishes in internal/session, which
// drives the production wake code (sendWakeNudgeNoWait) against this binary.
//
// "Types nothing" is shown by control: the target is a stopped session, so a
// send that passes the gate fails at the typing stage with "not running";
// the refused one never reaches it, and --ignore-budget does.
func TestBudgetGate_CLISendRefusesAndPublishes(t *testing.T) {
	home, id := budgetHome(t, enforcedSpentBudget, 5)
	_, stderr, code := runAgentDeck(t, home, "session", "send", id, "wake up", "-q")
	if code != 1 || !strings.Contains(stderr, "budget exceeded: daily global limit exceeded ($5.00 used of $1.00)") ||
		!strings.Contains(stderr, "nothing was sent to 'budget-target'") {
		t.Fatalf("refusal: exit %d stderr %q", code, stderr)
	}
	if strings.Contains(stderr, "not running") {
		t.Fatalf("reached the typing stage: %q", stderr)
	}
	frames := budgetBusFrames(t, home)
	if len(frames) != 1 {
		t.Fatalf("%d session.budget_exceeded frames, want exactly 1: %v", len(frames), frames)
	}
	data, _ := frames[0]["data"].(map[string]any)
	if frames[0]["session_id"] != id || data["via"] != "send" || data["reason"] != "daily global limit exceeded" ||
		data["used_usd"] != float64(5) || data["limit_usd"] != float64(1) {
		t.Fatalf("frame = %v, want session %s via send, 5 of 1 USD", frames[0], id)
	}

	// Control: --ignore-budget gets past the gate and publishes nothing.
	_, stderr, _ = runAgentDeck(t, home, "session", "send", id, "wake up", "-q", "--ignore-budget")
	if !strings.Contains(stderr, "not running") || strings.Contains(stderr, "budget exceeded") {
		t.Fatalf("control (--ignore-budget) should fail at typing with 'not running', got %q", stderr)
	}
	if n := len(budgetBusFrames(t, home)); n != 1 {
		t.Fatalf("control published a frame: %d frames", n)
	}
}
