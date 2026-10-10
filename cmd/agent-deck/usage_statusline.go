package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func cacheClaudeStatusline(profile string, payload []byte) error {
	var input struct {
		SessionID     string                           `json:"session_id"`
		Model         *statedb.ClaudeStatuslineModel   `json:"model"`
		Cwd           string                           `json:"cwd"`
		ContextWindow *statedb.ClaudeStatuslineContext `json:"context_window"`
		RateLimits    *statedb.ClaudeStatuslineLimits  `json:"rate_limits"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return fmt.Errorf("decoding statusline record: %w", err)
	}
	if input.SessionID == "" {
		return nil
	}
	record := statedb.ClaudeStatusline{
		ClaudeSessionID: input.SessionID, CapturedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Model: input.Model, Cwd: input.Cwd, ContextWindow: input.ContextWindow, RateLimits: input.RateLimits,
	}
	ownerProfile, ok, err := statuslineOwnerProfile(profile, input.SessionID)
	if err != nil || !ok {
		// No existing profile store to hold the record: keep only the quota
		// cache rather than materialising a profile store (#1790).
		return err
	}
	storage, err := session.NewStorageWithProfile(ownerProfile)
	if err != nil {
		return err
	}
	defer storage.Close()
	sessionID, err := storage.GetDB().SaveClaudeStatusline(record)
	if err != nil {
		return fmt.Errorf("saving statusline record: %w", err)
	}
	// Own this short-lived writer so wrapped commands (which may os.Exit) cannot
	// exit before the frame drains. This built-in deliberately bypasses plugins.
	bus := events.OpenProfile(storage.Profile())
	bus.Publish("usage.statusline", sessionID, record)
	return bus.Close()
}

func usageStatusline(profile string, args []string, out, diagnostic io.Writer) int {
	fs := flag.NewFlagSet("usage statusline", flag.ContinueOnError)
	fs.SetOutput(diagnostic)
	target := fs.String("session", "", "agent-deck session ID or title")
	_ = fs.Bool("json", false, "print the last statusline record as JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *target == "" || fs.NArg() != 0 {
		fmt.Fprintln(diagnostic, "Usage: agent-deck usage statusline --session <id|title> --json")
		return 2
	}
	storage, err := session.NewLiveReadOnlyStorageWithProfile(profile)
	if err == nil {
		defer storage.Close()
		record, readErr := storage.GetDB().ClaudeStatuslineForSession(*target)
		if readErr == nil {
			if json.NewEncoder(out).Encode(record) == nil {
				return 0
			}
			return 1
		}
	}
	fmt.Fprintln(out, `{"error":"no statusline record"}`)
	return 1
}

func handleUsageStatusline(profile string, args []string) {
	if code := usageStatusline(profile, args, os.Stdout, os.Stderr); code != 0 {
		exitCLI(code)
	}
}

// statuslineOwnerProfile picks the existing profile store that keeps a
// statusline record. Hook feed profiles name account slots, and the owning
// session may live in a different profile, so a native ID recorded in exactly
// one profile wins (only recorded IDs count, never cwd or titles). A record
// without a unique owner goes to the feed profile, else to the configured
// default profile, and only when that profile's store already exists: a slot
// name such as "work" or "default-claude-1" is a Claude account, not consent
// to create an agent-deck profile (#1790). ok is false when no store fits.
func statuslineOwnerProfile(feedProfile, nativeID string) (string, bool, error) {
	profiles, err := session.ListProfiles()
	if err != nil {
		return "", false, err
	}
	var owner string
	matches := 0
	for _, profile := range profiles {
		storage, err := session.NewLiveReadOnlyStorageWithProfile(profile)
		if err != nil {
			continue
		}
		var count int
		err = storage.GetDB().DB().QueryRow(`SELECT count(*) FROM instances WHERE tool = 'claude' AND json_extract(tool_data, '$.claude_session_id') = ?`, nativeID).Scan(&count)
		storage.Close()
		if err != nil || count == 0 {
			continue
		}
		if profile == feedProfile {
			return profile, true, nil
		}
		matches++
		owner = profile
	}
	if matches == 1 {
		return owner, true, nil
	}
	fallback := session.DefaultProfile
	if config, err := session.LoadConfig(); err == nil && config.DefaultProfile != "" {
		fallback = config.DefaultProfile
	}
	for _, candidate := range []string{feedProfile, fallback} {
		for _, existing := range profiles {
			if candidate != "" && candidate == existing {
				return candidate, true, nil
			}
		}
	}
	return "", false, nil
}

func handleUsageStatuslineWrap(profile string, args []string) {
	if statuslineHelpRequested(args) {
		fmt.Println("Usage: agent-deck usage statusline-wrap [-- <command> [args...]]")
		return
	}
	handleUsageIngestMode(profile, append([]string{"claude"}, args...), true)
}

func defaultClaudeStatusline(payload []byte) string {
	var input struct {
		Model         *statedb.ClaudeStatuslineModel   `json:"model"`
		ContextWindow *statedb.ClaudeStatuslineContext `json:"context_window"`
		RateLimits    *statedb.ClaudeStatuslineLimits  `json:"rate_limits"`
	}
	if json.Unmarshal(payload, &input) != nil {
		return "Claude"
	}
	model := "Claude"
	if input.Model != nil {
		if input.Model.DisplayName != "" {
			model = input.Model.DisplayName
		} else if input.Model.ID != "" {
			model = input.Model.ID
		}
	}
	// Payload strings are data, never terminal control sequences.
	model = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, model)
	parts := []string{model}
	if input.ContextWindow != nil && input.ContextWindow.UsedPercentage != nil {
		parts = append(parts, fmt.Sprintf("Context %.1f%%", *input.ContextWindow.UsedPercentage))
	}
	if input.RateLimits != nil {
		for _, entry := range []struct {
			label  string
			window *statedb.ClaudeStatuslineWindow
		}{{"5h", input.RateLimits.FiveHour}, {"7d", input.RateLimits.SevenDay}} {
			if entry.window != nil && entry.window.UsedPercentage != nil {
				parts = append(parts, fmt.Sprintf("%s %.1f%%", entry.label, *entry.window.UsedPercentage))
			}
		}
	}
	return strings.Join(parts, " | ")
}

func statuslineHelpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}
