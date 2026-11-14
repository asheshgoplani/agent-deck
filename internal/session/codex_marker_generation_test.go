package session

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Regression for the 2026-11-12 refusal "cannot reconcile Codex submission
// marker: invalid Codex submission marker turn generation".
//
// The persisted marker was NOT invalid — every field validated, and its own
// prior generation still parsed. What failed was the READ of the current
// generation: a long-running turn pushed its own turn-start record past the
// backward scan ceiling, so the caller passed "" and Reconcile validated ""
// with allowEmpty=false, blaming the marker.
//
// The contract this pins:
//   - a marker that recorded a generation, read against an undeterminable
//     current generation, returns ErrCodexGenerationUndeterminable, is never
//     described as invalid, and is left on disk;
//   - a marker created for a thread that had no turn yet keeps its own,
//     different "unresolved submission ... manually remove" message.
func TestReconcileCodexSubmissionMarker_UndeterminableGenerationIsNotAnInvalidMarker(t *testing.T) {
	isolateCodexSubmissionMarkers(t)

	created := time.Date(2026, 11, 12, 12, 4, 37, 0, time.UTC)
	marker, err := PrepareCodexSubmissionMarker(
		"instance-generation", "thread-generation",
		"thread-generation:turn-prior", created,
	)
	if err != nil {
		t.Fatal(err)
	}
	path, err := codexSubmissionMarkerPath("thread-generation")
	if err != nil {
		t.Fatal(err)
	}

	accepted, err := ReconcileCodexSubmissionMarker(
		"instance-generation", "thread-generation", "", nil,
	)
	if !errors.Is(err, ErrCodexGenerationUndeterminable) {
		t.Fatalf("want ErrCodexGenerationUndeterminable, got accepted=%q err=%v", accepted, err)
	}
	if accepted != "" {
		t.Fatalf("undeterminable generation was accepted as %q", accepted)
	}
	if strings.Contains(err.Error(), "invalid Codex submission marker") {
		t.Fatalf("the intact marker was blamed: %v", err)
	}
	if !strings.Contains(err.Error(), marker.AttemptID) {
		t.Fatalf("refusal does not name the submission it holds: %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("marker was removed instead of preserved: %v", statErr)
	}

	// The same marker reconciles normally once the generation is readable.
	accepted, err = ReconcileCodexSubmissionMarker(
		"instance-generation", "thread-generation",
		"thread-generation:turn-prior", func(string, time.Time) (bool, error) { return true, nil },
	)
	if err != nil || accepted != "thread-generation:turn-prior" {
		t.Fatalf("readable generation did not reconcile: accepted=%q err=%v", accepted, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("reconciled marker still exists: %v", statErr)
	}
}

// A marker created for a thread that had not started a turn yet carries no
// prior generation. That state keeps its established refusal — it is a genuine
// "an earlier send may still be in flight" case with real recovery guidance,
// not an unreadable rollout — so this fix must not swallow it.
func TestReconcileCodexSubmissionMarker_FreshThreadKeepsItsOwnRefusal(t *testing.T) {
	isolateCodexSubmissionMarkers(t)

	if _, err := PrepareCodexSubmissionMarker(
		"instance-fresh", "thread-fresh", "", time.Date(2026, 11, 12, 12, 4, 37, 0, time.UTC),
	); err != nil {
		t.Fatal(err)
	}
	path, err := codexSubmissionMarkerPath("thread-fresh")
	if err != nil {
		t.Fatal(err)
	}

	_, err = ReconcileCodexSubmissionMarker("instance-fresh", "thread-fresh", "", nil)
	if err == nil {
		t.Fatal("fresh-thread marker with no generation reconciled without evidence")
	}
	if errors.Is(err, ErrCodexGenerationUndeterminable) {
		t.Fatalf("fresh-thread marker reported as an unreadable rollout: %v", err)
	}
	if !strings.Contains(err.Error(), "manually remove") {
		t.Fatalf("fresh-thread refusal lost its recovery guidance: %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("fresh-thread marker was removed: %v", statErr)
	}
}
