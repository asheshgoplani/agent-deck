package feedback_test

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/feedback"
	"github.com/stretchr/testify/require"
)

// v1.7.41 — feedback prompt pacing for new users.
//
// BUG: Before v1.7.41, a brand-new user who launched agent-deck three
// times back-to-back was prompted all three times — no time-based or
// usage-based floor before the first ask. This suite gates every new
// pacing branch in ShouldShow and RecordLaunch/RecordShown.
//
// Pacing rules:
//   - First prompt requires BOTH:
//       FirstSeenAt at least MIN_DAYS (default 3) days in the past, AND
//       LaunchCount at least MIN_LAUNCHES (default 7).
//   - After a prompt is shown, skip for COOLDOWN_DAYS (default 14).
//   - Opt-out, already-rated-this-version, max-shows still win.
//   - Constants overridable for tests via
//       AGENTDECK_FEEDBACK_MIN_DAYS
//       AGENTDECK_FEEDBACK_MIN_LAUNCHES
//       AGENTDECK_FEEDBACK_COOLDOWN_DAYS

func baseEnabled() *feedback.State {
	return &feedback.State{
		LastRatedVersion: "",
		FeedbackEnabled:  true,
		ShownCount:       0,
		MaxShows:         3,
	}
}

// TEST-P1: brand new state (FirstSeenAt zero, LaunchCount zero) must block
// ShouldShow. RecordLaunch must set FirstSeenAt to now and bump LaunchCount.
func TestPacing_NewUser_FirstSeenSetOnRecordLaunch(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	st := baseEnabled()
	now := time.Now()

	require.True(t, st.FirstSeenAt.IsZero(), "precondition: FirstSeenAt starts zero")

	feedback.RecordLaunch(st, now)

	require.Equal(t, 1, st.LaunchCount)
	require.False(t, st.FirstSeenAt.IsZero(), "RecordLaunch must seed FirstSeenAt on first call")
	require.WithinDuration(t, now, st.FirstSeenAt, time.Second)

	// Even though FirstSeenAt is set, new user hasn't hit min-days or
	// min-launches thresholds, so ShouldShow must be false.
	require.False(t, feedback.ShouldShow(st, "1.7.41", now))
}

// TEST-P2: RecordLaunch must NOT overwrite a pre-existing FirstSeenAt.
// (Pacing decision must persist across restarts.)
func TestPacing_RecordLaunch_DoesNotOverwriteFirstSeenAt(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	original := time.Now().Add(-10 * 24 * time.Hour)
	st := baseEnabled()
	st.FirstSeenAt = original
	st.LaunchCount = 5

	feedback.RecordLaunch(st, time.Now())

	require.Equal(t, 6, st.LaunchCount)
	require.Equal(t, original, st.FirstSeenAt, "FirstSeenAt must be immutable after first set")
}

// TEST-P3: 1 day since FirstSeenAt, 3 launches — both thresholds unmet.
func TestPacing_1Day_3Launches_Blocked(t *testing.T) {
	now := time.Now()
	st := baseEnabled()
	st.FirstSeenAt = now.Add(-24 * time.Hour)
	st.LaunchCount = 3

	require.False(t, feedback.ShouldShow(st, "1.7.41", now))
}

// TEST-P4: 4 days, 10 launches — both thresholds met.
func TestPacing_4Days_10Launches_Shown(t *testing.T) {
	now := time.Now()
	st := baseEnabled()
	st.FirstSeenAt = now.Add(-4 * 24 * time.Hour)
	st.LaunchCount = 10

	require.True(t, feedback.ShouldShow(st, "1.7.41", now))
}

// TEST-P5: 4 days, 3 launches — launches threshold not met.
func TestPacing_4Days_3Launches_Blocked(t *testing.T) {
	now := time.Now()
	st := baseEnabled()
	st.FirstSeenAt = now.Add(-4 * 24 * time.Hour)
	st.LaunchCount = 3

	require.False(t, feedback.ShouldShow(st, "1.7.41", now))
}

// TEST-P6: 1 day, 10 launches — days threshold not met.
func TestPacing_1Day_10Launches_Blocked(t *testing.T) {
	now := time.Now()
	st := baseEnabled()
	st.FirstSeenAt = now.Add(-24 * time.Hour)
	st.LaunchCount = 10

	require.False(t, feedback.ShouldShow(st, "1.7.41", now))
}

// TEST-P7: after RecordShown, LastPromptedAt is set to now and further
// ShouldShow calls are blocked for the cooldown window.
func TestPacing_AfterShown_CooldownBlocks(t *testing.T) {
	now := time.Now()
	st := baseEnabled()
	st.FirstSeenAt = now.Add(-30 * 24 * time.Hour)
	st.LaunchCount = 50
	// thresholds met — ShouldShow would return true
	require.True(t, feedback.ShouldShow(st, "1.7.41", now))

	feedback.RecordShown(st, now)

	require.WithinDuration(t, now, st.LastPromptedAt, time.Second,
		"RecordShown must stamp LastPromptedAt so cooldown engages")
	require.Equal(t, 1, st.ShownCount)

	// 1 day later — still within 14-day cooldown.
	require.False(t, feedback.ShouldShow(st, "1.7.41", now.Add(24*time.Hour)))
	// 13 days later — still within 14-day cooldown.
	require.False(t, feedback.ShouldShow(st, "1.7.41", now.Add(13*24*time.Hour)))
}

// TEST-P8: 15 days after last prompt, ShouldShow returns true again.
func TestPacing_CooldownExpired_ShownAgain(t *testing.T) {
	now := time.Now()
	st := baseEnabled()
	st.FirstSeenAt = now.Add(-30 * 24 * time.Hour)
	st.LaunchCount = 50
	st.ShownCount = 1
	st.LastPromptedAt = now.Add(-15 * 24 * time.Hour)

	require.True(t, feedback.ShouldShow(st, "1.7.41", now))
}

// TEST-P9: env vars override the default constants.
func TestPacing_EnvOverride(t *testing.T) {
	t.Setenv("AGENTDECK_FEEDBACK_MIN_DAYS", "1")
	t.Setenv("AGENTDECK_FEEDBACK_MIN_LAUNCHES", "2")
	t.Setenv("AGENTDECK_FEEDBACK_COOLDOWN_DAYS", "1")

	now := time.Now()
	st := baseEnabled()
	st.FirstSeenAt = now.Add(-26 * time.Hour) // 1 day + 2h
	st.LaunchCount = 2

	require.True(t, feedback.ShouldShow(st, "1.7.41", now),
		"with env overrides, 26h + 2 launches must qualify")

	// RecordShown -> cooldown 1 day.
	feedback.RecordShown(st, now)
	require.False(t, feedback.ShouldShow(st, "1.7.41", now.Add(23*time.Hour)))
	require.True(t, feedback.ShouldShow(st, "1.7.41", now.Add(25*time.Hour)))
}

// TEST-P10: opt-out still wins over all pacing conditions.
func TestPacing_OptOutWinsOverPacing(t *testing.T) {
	now := time.Now()
	st := baseEnabled()
	st.FeedbackEnabled = false
	st.FirstSeenAt = now.Add(-365 * 24 * time.Hour)
	st.LaunchCount = 10_000

	require.False(t, feedback.ShouldShow(st, "1.7.41", now),
		"opt-out must beat every pacing threshold")
}

// TEST-P11: already-rated-this-version still wins.
func TestPacing_AlreadyRatedWinsOverPacing(t *testing.T) {
	now := time.Now()
	st := baseEnabled()
	st.LastRatedVersion = "1.7.41"
	st.FirstSeenAt = now.Add(-365 * 24 * time.Hour)
	st.LaunchCount = 10_000

	require.False(t, feedback.ShouldShow(st, "1.7.41", now))
}

// TEST-P12: max-shows still wins.
func TestPacing_MaxShowsWinsOverPacing(t *testing.T) {
	now := time.Now()
	st := baseEnabled()
	st.ShownCount = 3
	st.MaxShows = 3
	st.FirstSeenAt = now.Add(-365 * 24 * time.Hour)
	st.LaunchCount = 10_000

	require.False(t, feedback.ShouldShow(st, "1.7.41", now))
}

// TEST-P13: RecordRating must NOT reset FirstSeenAt or LastPromptedAt —
// new-version prompts still pace against the user's history.
func TestPacing_RecordRating_PreservesPacingFields(t *testing.T) {
	now := time.Now()
	originalFirst := now.Add(-30 * 24 * time.Hour)
	originalLast := now.Add(-1 * 24 * time.Hour)

	st := baseEnabled()
	st.FirstSeenAt = originalFirst
	st.LaunchCount = 50
	st.LastPromptedAt = originalLast
	st.ShownCount = 2

	feedback.RecordRating(st, "1.7.41", 4)

	require.Equal(t, "1.7.41", st.LastRatedVersion)
	require.Equal(t, 0, st.ShownCount, "existing contract: RecordRating resets ShownCount")
	require.Equal(t, originalFirst, st.FirstSeenAt,
		"RecordRating must NOT touch FirstSeenAt — pacing persists across versions")
	require.Equal(t, originalLast, st.LastPromptedAt,
		"RecordRating must NOT touch LastPromptedAt — pacing persists across versions")
	require.Equal(t, 50, st.LaunchCount, "RecordRating must NOT touch LaunchCount")
}

// TEST-P14: state round-trip through SaveState/LoadState must preserve
// the new pacing fields (RFC3339 JSON serialization).
func TestPacing_StateRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	now := time.Now().UTC().Truncate(time.Second)
	st := &feedback.State{
		LastRatedVersion: "1.7.40",
		FeedbackEnabled:  true,
		ShownCount:       1,
		MaxShows:         3,
		LaunchCount:      12,
		FirstSeenAt:      now.Add(-30 * 24 * time.Hour),
		LastPromptedAt:   now.Add(-2 * 24 * time.Hour),
	}
	require.NoError(t, feedback.SaveState(st))

	loaded, err := feedback.LoadState()
	require.NoError(t, err)
	require.Equal(t, 12, loaded.LaunchCount)
	require.True(t, st.FirstSeenAt.Equal(loaded.FirstSeenAt),
		"FirstSeenAt round-trip: want %v got %v", st.FirstSeenAt, loaded.FirstSeenAt)
	require.True(t, st.LastPromptedAt.Equal(loaded.LastPromptedAt),
		"LastPromptedAt round-trip: want %v got %v", st.LastPromptedAt, loaded.LastPromptedAt)
}
