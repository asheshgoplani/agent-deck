package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func issue2836Stamp(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000Z")
}

func issue2836TurnStart(turnID string, at time.Time) string {
	return `{"timestamp":"` + issue2836Stamp(at) + `","type":"event_msg","payload":{"type":"task_started","turn_id":"` + turnID + `"}}`
}

func issue2836UserItem(turnID string, at time.Time) string {
	turn := ""
	if turnID != "" {
		turn = `"turn_id":"` + turnID + `",`
	}
	return `{"timestamp":"` + issue2836Stamp(at) + `","type":"event_msg","payload":{"type":"item_completed",` + turn + `"item":{"type":"UserMessage","id":"item"}}}`
}

func issue2836UserEvent(at time.Time) string {
	return `{"timestamp":"` + issue2836Stamp(at) + `","type":"event_msg","payload":{"type":"user_message","message":"text"}}`
}

// Issue #2836: input sent while a turn runs is recorded inside that turn.
func TestIssue2836CodexTurnTookInputSince(t *testing.T) {
	start := time.Date(2026, 10, 21, 18, 0, 39, 871_000_000, time.UTC)
	since := start.Add(900 * time.Millisecond) // the marker's creation
	later := since.Add(13 * time.Second)
	tests := []struct {
		name  string
		lines []string
		want  bool
	}{
		{
			name: "later input in the running turn",
			lines: []string{
				issue2836TurnStart("turn-t", start),
				issue2836UserItem("turn-t", start.Add(250*time.Millisecond)),
				`{"timestamp":"` + issue2836Stamp(later) + `","type":"response_item","payload":{"type":"message","role":"user"}}`,
				issue2836UserItem("turn-t", later),
				`{"timestamp":"` + issue2836Stamp(later.Add(time.Minute)) + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-t"}}`,
			},
			want: true,
		},
		{
			name: "later input with no turn id, as current Codex writes it",
			lines: []string{
				issue2836TurnStart("turn-t", start),
				issue2836UserItem("", start.Add(250*time.Millisecond)),
				issue2836UserItem("", later),
			},
			want: true,
		},
		{
			name: "older user_message events",
			lines: []string{
				issue2836TurnStart("turn-t", start),
				issue2836UserEvent(start.Add(250 * time.Millisecond)),
				issue2836UserEvent(later),
			},
			want: true,
		},
		{
			name: "only the opening input, recorded after the marker",
			lines: []string{
				issue2836TurnStart("turn-t", start),
				issue2836UserItem("turn-t", since.Add(time.Millisecond)),
			},
		},
		{
			name: "one opening input written in both encodings",
			lines: []string{
				issue2836TurnStart("turn-t", start),
				issue2836UserEvent(since.Add(time.Millisecond)),
				issue2836UserItem("turn-t", since.Add(2*time.Millisecond)),
			},
		},
		{
			name: "later input recorded before the marker",
			lines: []string{
				issue2836TurnStart("turn-t", start),
				issue2836UserItem("turn-t", start.Add(250*time.Millisecond)),
				issue2836UserItem("turn-t", since.Add(-time.Millisecond)),
			},
		},
		{
			name: "a newer turn started",
			lines: []string{
				issue2836TurnStart("turn-t", start),
				issue2836UserItem("turn-t", start.Add(250*time.Millisecond)),
				issue2836UserItem("turn-t", later),
				issue2836TurnStart("turn-next", later.Add(time.Second)),
				issue2836UserItem("turn-next", later.Add(2*time.Second)),
			},
		},
		{
			name: "a newer turn started, its inputs untagged",
			lines: []string{
				issue2836TurnStart("turn-t", start),
				issue2836UserItem("", start.Add(250*time.Millisecond)),
				issue2836TurnStart("turn-next", later),
				issue2836UserItem("", later.Add(time.Second)),
				issue2836UserItem("", later.Add(2*time.Second)),
			},
		},
		{
			name: "later input tagged with another turn",
			lines: []string{
				issue2836TurnStart("turn-t", start),
				issue2836UserItem("turn-t", start.Add(250*time.Millisecond)),
				issue2836UserItem("turn-other", later),
			},
		},
		{
			name: "no turn start",
			lines: []string{
				issue2836UserItem("turn-t", start.Add(250*time.Millisecond)),
				issue2836UserItem("turn-t", later),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(tt.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := codexTurnTookInputSinceFromRollout(path, "turn-t", since)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("took input = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIssue2836CodexTurnTookInputFailsClosedPastScanWindow(t *testing.T) {
	start := time.Date(2026, 10, 21, 18, 0, 39, 0, time.UTC)
	since := start.Add(time.Second)
	var b strings.Builder
	b.WriteString(issue2836TurnStart("turn-t", start) + "\n")
	b.WriteString(issue2836UserItem("turn-t", start.Add(250*time.Millisecond)) + "\n")
	padding := `{"type":"event_msg","payload":{"type":"agent_message","message":"` + strings.Repeat("x", 1024) + `"}}` + "\n"
	for int64(b.Len()) <= codexTurnGenerationScanMaxBytes {
		b.WriteString(padding)
	}
	b.WriteString(issue2836UserItem("turn-t", since.Add(time.Second)) + "\n")
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := codexTurnTookInputSinceFromRollout(path, "turn-t", since)
	if err != nil || got {
		t.Fatalf("turn start past the scan window: took = %v, err = %v; want false", got, err)
	}
}

func TestIssue2836ReconcileClearsOnlyWithMidTurnInput(t *testing.T) {
	isolateCodexSubmissionMarkers(t)
	createdAt := time.Date(2026, 10, 21, 18, 0, 40, 762_000_000, time.UTC)
	marker, err := PrepareCodexSubmissionMarker("instance-2836", "thread-2836", "thread-2836:turn-t", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := marker.MarkTransportAmbiguous(createdAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	path, err := codexSubmissionMarkerPath(marker.CodexSessionID)
	if err != nil {
		t.Fatal(err)
	}
	reconcile := func(evidence func(string, time.Time) (bool, error)) (string, error) {
		return ReconcileCodexSubmissionMarker(marker.InstanceID, marker.CodexSessionID, marker.PriorTurnGeneration, evidence)
	}
	refuses := func(name string, evidence func(string, time.Time) (bool, error)) error {
		t.Helper()
		accepted, err := reconcile(evidence)
		if err == nil || accepted != "" || !strings.Contains(err.Error(), "manually remove") {
			t.Fatalf("%s: accepted=%q err=%v, want the refusal", name, accepted, err)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("%s: marker was removed: %v", name, statErr)
		}
		return err
	}

	refuses("no evidence source", nil)
	var askedGen string
	var askedSince time.Time
	refuses("no later input", func(gen string, since time.Time) (bool, error) {
		askedGen, askedSince = gen, since
		return false, nil
	})
	if askedGen != marker.PriorTurnGeneration || !askedSince.Equal(createdAt) {
		t.Fatalf("evidence asked for (%q, %v), want (%q, %v)", askedGen, askedSince, marker.PriorTurnGeneration, createdAt)
	}
	if err := refuses("unreadable rollout", func(string, time.Time) (bool, error) {
		return true, errors.New("rollout unreadable")
	}); !strings.Contains(err.Error(), "rollout unreadable") {
		t.Fatalf("refusal hid the read error: %v", err)
	}

	accepted, err := reconcile(func(string, time.Time) (bool, error) { return true, nil })
	if err != nil || accepted != marker.PriorTurnGeneration {
		t.Fatalf("mid-turn input did not reconcile: accepted=%q err=%v", accepted, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("reconciled marker still exists: %v", statErr)
	}
}

func TestIssue2836MarkerWithoutTurnNeverAsksForInput(t *testing.T) {
	isolateCodexSubmissionMarkers(t)
	marker, err := PrepareCodexSubmissionMarker("instance-fresh", "thread-fresh", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := ReconcileCodexSubmissionMarker(marker.InstanceID, marker.CodexSessionID, "",
		func(string, time.Time) (bool, error) {
			t.Fatal("a thread with no turn has no turn to take input up")
			return true, nil
		})
	if err == nil || accepted != "" {
		t.Fatalf("marker without a turn reconciled: accepted=%q err=%v", accepted, err)
	}
}
