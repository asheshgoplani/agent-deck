// replay2469 replays the issue #2469 turn classifier over a real Claude
// transcript, turn by turn, and reports what the new producer would have
// emitted: records by tier, noise, wakeups. Measurement aid, not shipped.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: replay2469 <transcript.jsonl> [fromRFC3339 toRFC3339]")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var from, to time.Time
	if len(os.Args) >= 4 {
		from, _ = time.Parse(time.RFC3339, os.Args[2])
		to, _ = time.Parse(time.RFC3339, os.Args[3])
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	// Walk forward; at every main-chain assistant record with text (a turn
	// end as the Stop hook would see it) classify the prefix.
	type rec struct {
		Type        string `json:"type"`
		IsSidechain bool   `json:"isSidechain"`
		Timestamp   string `json:"timestamp"`
		Message     struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	var prev *session.TurnJournalEntry
	counts := map[string]int{}
	trig := map[string]int{}
	wakes := 0
	textBytes := 0
	turns := 0
	for i, line := range lines {
		var r rec
		if json.Unmarshal([]byte(line), &r) != nil || r.IsSidechain || r.Type != "assistant" {
			continue
		}
		ts, _ := time.Parse(time.RFC3339Nano, r.Timestamp)
		if !from.IsZero() && (ts.Before(from) || ts.After(to)) {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(r.Message.Content, &blocks)
		hasText := false
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				hasText = true
				break
			}
		}
		if !hasText {
			continue
		}
		// Simulate the Stop at this record: classify the tail up to here.
		start := i - 200
		if start < 0 {
			start = 0
		}
		facts := session.ClassifyTranscriptTailForReplay(lines[start : i+1])
		if facts.Pending {
			continue
		}
		turns++
		tier := session.ClassifyTurnTier(facts, "waiting", prev)
		counts[tier]++
		trig[facts.Trigger]++
		if tier != session.TurnTierNoise {
			text := session.CapTurnText(facts.Text, session.DefaultTurnTextBytes)
			textBytes += len(text)
			prev = &session.TurnJournalEntry{Status: "waiting", Tier: tier, TextHash: facts.TextHash, UUID: facts.UUID}
			if facts.HasDone {
				prev.DoneStatus, prev.DoneSummary = facts.Done.Status, facts.Done.Summary
			}
			if tier == session.TurnTierUrgent {
				wakes++
			}
			fmt.Printf("%s %-6s %-7s %s\n", ts.Format("15:04:05"), tier, facts.Trigger, session.CapTurnText(strings.ReplaceAll(facts.Text, "\n", " "), 100))
		}
	}
	fmt.Printf("\nturns=%d tiers=%v triggers=%v urgent_wakes=%d text_bytes=%d\n", turns, counts, trig, wakes, textBytes)
}
