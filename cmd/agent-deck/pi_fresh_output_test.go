package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestWaitForFreshOutputPi(t *testing.T) {
	for _, scenario := range []string{"fresh", "stale", "recent previous turn", "missing timestamp"} {
		t.Run(scenario, func(t *testing.T) {
			fresh := scenario == "fresh"
			home := t.TempDir()
			t.Setenv("HOME", home)
			dir := filepath.Join(home, ".pi", "agent-deck", "pi-fresh")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "session.jsonl")
			record := func(ts, text string) string {
				return fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n", ts, text)
			}
			timestamp := "2026-01-01T00:00:00Z"
			if scenario == "recent previous turn" {
				timestamp = "2026-08-28T06:41:19.900Z"
			}
			if scenario == "missing timestamp" {
				timestamp = ""
			}
			if err := os.WriteFile(path, []byte(record(timestamp, "old answer")), 0600); err != nil {
				t.Fatal(err)
			}
			setFastFreshOutputConfig(t, 400*time.Millisecond)
			sentAt := time.Date(2026, 8, 28, 6, 41, 20, 0, time.UTC)
			if fresh {
				done := make(chan error, 1)
				go func() {
					time.Sleep(100 * time.Millisecond)
					f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
					if err != nil {
						done <- err
						return
					}
					_, err = f.WriteString(record(sentAt.Add(time.Second).Format(time.RFC3339Nano), "fresh answer"))
					closeErr := f.Close()
					if err == nil {
						err = closeErr
					}
					done <- err
				}()
				t.Cleanup(func() {
					if err := <-done; err != nil {
						t.Error(err)
					}
				})
			}
			got, err := waitForFreshOutput(&session.Instance{ID: "pi-fresh", Tool: "pi"}, sentAt, nil)
			if fresh {
				if err != nil {
					t.Fatal(err)
				}
				if got.Content != "fresh answer" {
					t.Fatalf("got stale answer: %+v", got)
				}
			} else if err == nil || got != nil || !strings.Contains(err.Error(), "freshness timeout") {
				t.Fatalf("missing fresh reply must fail, got %+v, %v", got, err)
			}
		})
	}
}
