package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSocketWaitTranscriptPollDeadline(t *testing.T) {
	for _, phase := range []string{"observed-turn", "turn-response"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			var err error
			if phase == "observed-turn" {
				_, err = AwaitObservedTurn(TurnQuery{Path: path}, 20*time.Millisecond, time.Second)
			} else {
				_, err = AwaitTurnResponse(TurnIdentity{Path: path, UUID: "peer"}, 20*time.Millisecond, time.Second)
			}
			if err == nil {
				t.Fatal("empty transcript must not satisfy either wait phase")
			}
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Fatalf("transcript poll exceeded its 20ms deadline: %s", elapsed)
			}
		})
	}
}
