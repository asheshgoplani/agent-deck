package main

import (
	"fmt"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// triggersSeedHost is the host every seeded trigger names. The panel header
// shows the rows' host, so a fixed name keeps the frame independent of the
// runner's hostname.
const triggersSeedHost = "labbox"

// seedTriggers writes a fixed set of triggers into the sandbox's state DB
// while the TUI under test is running: the same tables `session send
// --source` and `triggers scan` fill, written directly because neither CLI
// verb exists in this branch. The TUI's background poll sees the
// triggers_rev bump and loads them, which is the live-update path itself.
//
// Targets are gallery titles (claude-waiting, claude-idle, claude-running)
// plus one title that does not exist, so the panel shows every health:
// ok, failing, dead target, never fired, and a noisy watcher.
func (s *suite) seedTriggers() error {
	dbPath, err := s.findStateDB()
	if err != nil {
		return err
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		return err
	}
	// A retried step must not add a second round of fires (the counts and
	// no-op shares are part of the frame).
	if row, err := db.GetTrigger("watcher:github"); err == nil && row != nil {
		return nil
	}
	if err := db.ReplaceScannedTriggers([]statedb.TriggerRow{
		{ID: "systemd:agentdeck-overnight", Kind: triggers.KindSystemd, Source: "agentdeck-overnight", Host: triggersSeedHost,
			ConfigPath: "~/.config/systemd/user/agentdeck-overnight.service", Verb: "send",
			Target: "overnight-orchestrator", TargetBy: "title", Schedule: "every 5m", Unit: "agentdeck-overnight"},
		{ID: "systemd:mail-poll", Kind: triggers.KindSystemd, Source: "mail-poll", Host: triggersSeedHost,
			ConfigPath: "~/.config/systemd/user/mail-poll.service", Verb: "send",
			Target: "claude-waiting", TargetBy: "title", Schedule: "every 10m", Unit: "mail-poll"},
		{ID: "systemd:issue-poll", Kind: triggers.KindSystemd, Source: "issue-poll", Host: triggersSeedHost,
			ConfigPath: "~/.config/systemd/user/issue-poll.service", Verb: "send",
			Target: "claude-idle", TargetBy: "title", Schedule: "every 15m", Unit: "issue-poll"},
		{ID: "launchd:com.example.limit-guard", Kind: triggers.KindLaunchd, Source: "com.example.limit-guard", Host: triggersSeedHost,
			ConfigPath: "~/Library/LaunchAgents/com.example.limit-guard.plist", Verb: "send",
			Target: "claude-running", TargetBy: "title", Schedule: "every 1h", Unit: "com.example.limit-guard"},
	}); err != nil {
		return fmt.Errorf("seed scanned triggers: %w", err)
	}
	if err := db.UpsertPushedTrigger(statedb.TriggerRow{
		ID: "watcher:github", Kind: triggers.KindWatcher, Source: "github", Host: triggersSeedHost, Verb: "send",
		Target: "claude-idle", TargetBy: "title",
	}); err != nil {
		return fmt.Errorf("seed pushed trigger: %w", err)
	}
	// Fire times sit just past a minute boundary (5m05s, not 5m) so the
	// rendered age keeps its digit count for the whole capture, retries
	// included; the scrubber turns ages into <age> but keeps the padding.
	now := time.Now()
	fire := func(id string, ago time.Duration, outcome, line string) error {
		_, err := db.RecordTriggerFire(statedb.TriggerFireRow{
			TriggerID: id, TS: now.Add(-ago), Outcome: outcome, Line: line, TextHash: triggers.TextHash(line),
		})
		return err
	}
	type f struct {
		id      string
		ago     time.Duration
		outcome string
		line    string
	}
	var fires []f
	for i := 12; i >= 1; i-- {
		fires = append(fires, f{"systemd:agentdeck-overnight", time.Duration(i)*5*time.Minute + 5*time.Second, triggers.OutcomeDeadTarget, "tick: 0 waiting jobs"})
	}
	fires = append(fires,
		f{"systemd:mail-poll", 50*time.Minute + 5*time.Second, triggers.OutcomeDelivered, "3 new mails from billing"},
		f{"systemd:mail-poll", 30*time.Minute + 5*time.Second, triggers.OutcomeDelivered, "1 new mail from ops"},
		f{"systemd:mail-poll", 10*time.Minute + 5*time.Second, triggers.OutcomeDelivered, "2 new mails from leads"},
		f{"systemd:issue-poll", 45*time.Minute + 5*time.Second, triggers.OutcomeDelivered, "issue 812 opened"},
		f{"systemd:issue-poll", 15*time.Minute + 5*time.Second, triggers.OutcomeFailed, "issue 813 opened"},
	)
	for i := 10; i >= 1; i-- {
		fires = append(fires, f{"watcher:github", time.Duration(i)*90*time.Minute + 30*time.Minute, triggers.OutcomeDelivered, "CI green on main"})
	}
	for _, x := range fires {
		if err := fire(x.id, x.ago, x.outcome, x.line); err != nil {
			return fmt.Errorf("seed fire %s: %w", x.id, err)
		}
	}
	return nil
}
