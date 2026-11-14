package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression for the 2026-11-12 coordinator-rollout refusal: a turn that has
// been emitting output for a long time pushes its OWN turn_started record far
// from EOF, so the default 8 MiB backward ceiling finds no turn start and the
// caller is told a turn does not exist. Measured live on a session whose single
// turn had run ~55 minutes in a 293 MiB rollout: its turn start was 9,437,184
// bytes from EOF.
//
// The fix is a budgeted variant (LatestCodexTurnGenerationWithBudget, used by
// the Codex acceptance path at 64 MiB). The general default is deliberately
// unchanged, which this test pins: the narrow read must still report "no turn"
// for exactly the same file the wide read resolves.
func TestLatestCodexTurnIDFromRolloutWithin_ReadsPastTheDefaultCeiling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-generation.jsonl")

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// The newest (only) turn start, near the top of the file.
	if _, err := f.WriteString(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-long"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	// Filler between the turn start and EOF: invalid JSON lines are skipped by
	// the scanner, so this is purely distance. 10 MiB > the 8 MiB ceiling.
	line := strings.Repeat("x", 998) + "\n"
	for i := 0; i < 10500; i++ {
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	// A parseable, non-turn record last, so the newest record is not the filler.
	if _, err := f.WriteString(`{"type":"event_msg","payload":{"type":"agent_message"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= codexTurnGenerationScanMaxBytes {
		t.Fatalf("fixture is only %d bytes; it must exceed the %d-byte default ceiling",
			info.Size(), codexTurnGenerationScanMaxBytes)
	}

	if got, err := latestCodexTurnIDFromRollout(path); err != nil || got != "" {
		t.Fatalf("default ceiling: got turn %q, err %v; want \"\" (the start is beyond %d bytes)",
			got, err, codexTurnGenerationScanMaxBytes)
	}

	got, err := latestCodexTurnIDFromRolloutWithin(path, 64<<20)
	if err != nil {
		t.Fatalf("wide budget returned an error: %v", err)
	}
	if got != "turn-long" {
		t.Fatalf("wide budget found turn %q, want %q", got, "turn-long")
	}
}
