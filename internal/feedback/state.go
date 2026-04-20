package feedback

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// State holds the persisted feedback preferences for a user.
// File: ~/.agent-deck/feedback-state.json. Always serializes all fields (D-05).
//
// v1.7.41 added LaunchCount, FirstSeenAt, LastPromptedAt to pace the first
// prompt for new users. Serialized via RFC3339 through time.Time's MarshalJSON.
type State struct {
	LastRatedVersion string    `json:"last_rated_version"`
	FeedbackEnabled  bool      `json:"feedback_enabled"`
	ShownCount       int       `json:"shown_count"`
	MaxShows         int       `json:"max_shows"`
	LaunchCount      int       `json:"launch_count,omitempty"`
	FirstSeenAt      time.Time `json:"first_seen_at,omitempty"`
	LastPromptedAt   time.Time `json:"last_prompted_at,omitempty"`
}

// v1.7.41 pacing defaults. First prompt appears only once the user has used
// agent-deck for MinDaysBeforeFirstPrompt days AND across MinLaunchesBeforeFirstPrompt
// process starts; subsequent prompts are throttled by PromptCooldownDays.
const (
	defaultMinDaysBeforeFirstPrompt     = 3
	defaultMinLaunchesBeforeFirstPrompt = 7
	defaultPromptCooldownDays           = 14
)

// Env vars let tests override the pacing constants. They're intentionally
// undocumented in README — they exist for the test harness, not users.
const (
	envMinDays      = "AGENTDECK_FEEDBACK_MIN_DAYS"
	envMinLaunches  = "AGENTDECK_FEEDBACK_MIN_LAUNCHES"
	envCooldownDays = "AGENTDECK_FEEDBACK_COOLDOWN_DAYS"
)

// defaultState returns an initialized State with safe defaults.
func defaultState() *State {
	return &State{
		FeedbackEnabled: true,
		MaxShows:        3,
	}
}

// agentDeckDir returns the base agent-deck directory (~/.agent-deck).
// Inlined here to avoid importing internal/session, which is a heavyweight
// package and would create a circular import risk if session ever imports feedback.
func agentDeckDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("feedback: get home dir: %w", err)
	}
	return filepath.Join(home, ".agent-deck"), nil
}

// statePath returns the absolute path to the feedback state file.
func statePath() (string, error) {
	dir, err := agentDeckDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "feedback-state.json"), nil
}

// LoadState reads ~/.agent-deck/feedback-state.json and returns the state.
// If the file does not exist, it returns a default State (FeedbackEnabled=true, MaxShows=3).
// A missing file is NOT an error. A malformed file returns a default state to prevent crashes.
func LoadState() (*State, error) {
	path, err := statePath()
	if err != nil {
		return defaultState(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultState(), nil
		}
		return defaultState(), nil
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		// Malformed file — return default to prevent crash (T-01-03)
		return defaultState(), nil
	}
	return &s, nil
}

// SaveState atomically writes the state to ~/.agent-deck/feedback-state.json.
// Uses tmp+rename to prevent partial writes (T-01-01).
func SaveState(s *State) error {
	dir, err := agentDeckDir()
	if err != nil {
		return fmt.Errorf("feedback: get agent-deck dir: %w", err)
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("feedback: create dir: %w", err)
	}

	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("feedback: marshal state: %w", err)
	}

	path := filepath.Join(dir, "feedback-state.json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("feedback: write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("feedback: rename tmp: %w", err)
	}
	return nil
}

// envInt reads an integer env var, returning fallback on empty or invalid.
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

// MinDaysBeforeFirstPrompt returns the configured floor (default 3).
func MinDaysBeforeFirstPrompt() int {
	return envInt(envMinDays, defaultMinDaysBeforeFirstPrompt)
}

// MinLaunchesBeforeFirstPrompt returns the configured floor (default 7).
func MinLaunchesBeforeFirstPrompt() int {
	return envInt(envMinLaunches, defaultMinLaunchesBeforeFirstPrompt)
}

// PromptCooldownDays returns the configured cooldown (default 14).
func PromptCooldownDays() int {
	return envInt(envCooldownDays, defaultPromptCooldownDays)
}

// ShouldShow returns true only when every gate is clear:
//  1. feedback_enabled is true
//  2. last_rated_version does not match currentVersion
//  3. shown_count < max_shows
//  4. user has been around for at least MinDaysBeforeFirstPrompt days
//     AND has launched agent-deck at least MinLaunchesBeforeFirstPrompt times
//  5. no prompt was shown within the last PromptCooldownDays days
//
// Pure: never mutates state. Callers that want to track first-seen should
// call RecordLaunch at process start.
func ShouldShow(s *State, currentVersion string, now time.Time) bool {
	if !s.FeedbackEnabled {
		return false
	}
	if s.LastRatedVersion == currentVersion {
		return false
	}
	if s.ShownCount >= s.MaxShows {
		return false
	}

	// If no FirstSeenAt yet (RecordLaunch hasn't run this process), block.
	// The TUI's RecordLaunch call at startup seeds this on the very first
	// launch, so thereafter this branch only fires for broken callers.
	if s.FirstSeenAt.IsZero() {
		return false
	}
	minDays := MinDaysBeforeFirstPrompt()
	if now.Sub(s.FirstSeenAt) < time.Duration(minDays)*24*time.Hour {
		return false
	}
	if s.LaunchCount < MinLaunchesBeforeFirstPrompt() {
		return false
	}
	if !s.LastPromptedAt.IsZero() {
		cooldown := time.Duration(PromptCooldownDays()) * 24 * time.Hour
		if now.Sub(s.LastPromptedAt) < cooldown {
			return false
		}
	}
	return true
}

// RecordLaunch increments LaunchCount by 1 and seeds FirstSeenAt with now
// on the very first call. Subsequent calls never overwrite FirstSeenAt so
// pacing persists across version upgrades and state reloads.
// Does NOT save — caller must call SaveState.
func RecordLaunch(s *State, now time.Time) {
	s.LaunchCount++
	if s.FirstSeenAt.IsZero() {
		s.FirstSeenAt = now
	}
}

// RecordShown increments shown_count by 1 and stamps LastPromptedAt with now
// so the cooldown engages for subsequent calls. Does NOT save — caller
// must call SaveState.
func RecordShown(s *State, now time.Time) {
	s.ShownCount++
	s.LastPromptedAt = now
}

// RecordRating sets last_rated_version to currentVersion and resets shown_count
// to 0. Deliberately does NOT touch FirstSeenAt, LastPromptedAt, or LaunchCount —
// pacing signals survive a rating so the next version still paces against the
// user's real history. Does NOT save — caller must call SaveState.
func RecordRating(s *State, currentVersion string, rating int) {
	s.LastRatedVersion = currentVersion
	s.ShownCount = 0
	_ = rating // rating is used by the caller for display/formatting; stored externally
}

// RecordOptOut sets feedback_enabled to false (permanent opt-out).
// Does NOT save — caller must call SaveState.
func RecordOptOut(s *State) {
	s.FeedbackEnabled = false
}

// RatingEmoji maps a numeric rating (1-5) to an emoji.
// Returns "" for out-of-range values.
func RatingEmoji(rating int) string {
	switch rating {
	case 1:
		return "😞"
	case 2:
		return "😐"
	case 3:
		return "🙂"
	case 4:
		return "😀"
	case 5:
		return "🤩"
	default:
		return ""
	}
}

// FormatComment formats a feedback submission for posting to GitHub Discussions.
// Format: "**vVER** | **N/5** EMOJI | GOOS GOARCH\nCOMMENT"
// When comment is empty, the trailing newline and comment are omitted.
func FormatComment(version string, rating int, goos, goarch, comment string) string {
	header := fmt.Sprintf("**v%s** | **%d/5** %s | %s %s", version, rating, RatingEmoji(rating), goos, goarch)
	if comment == "" {
		return header
	}
	return header + "\n" + comment
}
