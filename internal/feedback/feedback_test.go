package feedback_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/feedback"
	"github.com/stretchr/testify/require"
)

// oldShouldShowBypass returns a state pre-seeded so it passes v1.7.41 pacing
// gates. Keeps the pre-v1.7.41 tests focused on their original assertions
// (enabled / not-rated / under-max) without having to pace every fixture.
func oldShouldShowBypass(s *feedback.State) *feedback.State {
	s.FirstSeenAt = time.Now().Add(-365 * 24 * time.Hour)
	s.LaunchCount = 10_000
	return s
}

// TEST-01: ShouldShow returns true when this is a new version
func TestShouldShow_NewVersion(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	st := oldShouldShowBypass(&feedback.State{
		LastRatedVersion: "1.0.0",
		FeedbackEnabled:  true,
		ShownCount:       0,
		MaxShows:         3,
	})
	require.NoError(t, feedback.SaveState(st))

	loaded, err := feedback.LoadState()
	require.NoError(t, err)
	require.True(t, feedback.ShouldShow(loaded, "1.5.1", time.Now()))
}

// TEST-02: ShouldShow returns false when already rated this version
func TestShouldShow_AlreadyRated(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	st := oldShouldShowBypass(&feedback.State{
		LastRatedVersion: "1.5.1",
		FeedbackEnabled:  true,
		ShownCount:       0,
		MaxShows:         3,
	})
	require.NoError(t, feedback.SaveState(st))

	loaded, err := feedback.LoadState()
	require.NoError(t, err)
	require.False(t, feedback.ShouldShow(loaded, "1.5.1", time.Now()))
}

// TEST-03: ShouldShow returns false when user opted out
func TestShouldShow_OptedOut(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	st := oldShouldShowBypass(&feedback.State{
		LastRatedVersion: "1.0.0",
		FeedbackEnabled:  false,
		ShownCount:       0,
		MaxShows:         3,
	})
	require.NoError(t, feedback.SaveState(st))

	loaded, err := feedback.LoadState()
	require.NoError(t, err)
	require.False(t, feedback.ShouldShow(loaded, "1.5.1", time.Now()))
}

// TEST-04: ShouldShow returns false when shown_count >= max_shows
func TestShouldShow_MaxShows(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	st := oldShouldShowBypass(&feedback.State{
		LastRatedVersion: "1.0.0",
		FeedbackEnabled:  true,
		ShownCount:       3,
		MaxShows:         3,
	})
	require.NoError(t, feedback.SaveState(st))

	loaded, err := feedback.LoadState()
	require.NoError(t, err)
	require.False(t, feedback.ShouldShow(loaded, "1.5.1", time.Now()))
}

// TEST-05: RecordRating sets last_rated_version and resets shown_count
func TestRecordRating_Valid(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	st := &feedback.State{
		LastRatedVersion: "1.0.0",
		FeedbackEnabled:  true,
		ShownCount:       2,
		MaxShows:         3,
	}
	feedback.RecordRating(st, "1.5.1", 4)
	require.NoError(t, feedback.SaveState(st))

	loaded, err := feedback.LoadState()
	require.NoError(t, err)
	require.Equal(t, "1.5.1", loaded.LastRatedVersion)
	require.Equal(t, 0, loaded.ShownCount)
}

// TEST-06: RecordOptOut sets feedback_enabled to false (persisted)
func TestRecordOptOut(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	st := &feedback.State{
		LastRatedVersion: "1.0.0",
		FeedbackEnabled:  true,
		ShownCount:       0,
		MaxShows:         3,
	}
	feedback.RecordOptOut(st, "1.5.1")
	require.NoError(t, feedback.SaveState(st))

	loaded, err := feedback.LoadState()
	require.NoError(t, err)
	require.False(t, loaded.FeedbackEnabled)
}

// TEST-07: RecordShown increments shown_count (persisted)
func TestRecordShown(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	st := &feedback.State{
		LastRatedVersion: "1.0.0",
		FeedbackEnabled:  true,
		ShownCount:       0,
		MaxShows:         3,
	}
	feedback.RecordShown(st, time.Now())
	require.NoError(t, feedback.SaveState(st))

	loaded, err := feedback.LoadState()
	require.NoError(t, err)
	require.Equal(t, 1, loaded.ShownCount)
}

// TEST-08: FormatComment returns exact formatted string
func TestFormatComment(t *testing.T) {
	result := feedback.FormatComment("1.5.1", 4, "darwin", "arm64", "scrollback fix")
	require.Equal(t, "**v1.5.1** | **4/5** 😀 | darwin arm64\nscrollback fix", result)
}

// TEST-09: RatingEmoji maps 1-5 to correct emojis
func TestRatingEmoji(t *testing.T) {
	require.Equal(t, "😞", feedback.RatingEmoji(1))
	require.Equal(t, "😐", feedback.RatingEmoji(2))
	require.Equal(t, "🙂", feedback.RatingEmoji(3))
	require.Equal(t, "😀", feedback.RatingEmoji(4))
	require.Equal(t, "🤩", feedback.RatingEmoji(5))
}

// fakeExitError simulates exec.ExitError with a configurable exit code.
type fakeExitError struct{ code int }

func (e *fakeExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e *fakeExitError) ExitCode() int { return e.code }

// TEST-10: TestSend_GhAuthFailure verifies non-headless fallback copies to clipboard AND opens browser
func TestSend_GhAuthFailure(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	clipboardCalled := false
	clipboardText := ""
	browserCalled := false

	s := feedback.NewSender()
	// ghCmd returns exit code 4 (auth failure)
	s.GhCmd = func(args ...string) error {
		return &fakeExitError{code: 4}
	}
	// ClipboardCmd records the body it receives
	s.ClipboardCmd = func(text string) error {
		clipboardCalled = true
		clipboardText = text
		return nil
	}
	// BrowserCmd records whether it was called
	s.BrowserCmd = func(url string) error {
		browserCalled = true
		return nil
	}
	// Not headless — both clipboard and browser should fire
	s.IsHeadlessFunc = func() bool { return false }

	err := s.Send("1.5.1", 4, "darwin", "arm64", "test comment")
	require.NoError(t, err)
	require.True(t, clipboardCalled, "clipboard must be called with formatted body before opening browser")
	require.True(t, browserCalled, "browser fallback should open the Discussion URL after clipboard copy")
	require.Contains(t, clipboardText, "v1.5.1", "clipboard body must contain the version")
	require.NotContains(t, clipboardText, "github.com", "clipboard must contain the comment body, not a URL")
}

// TEST-11: TestSend_Headless verifies headless mode copies to clipboard only (no browser)
func TestSend_Headless(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	clipboardCalled := false
	browserCalled := false

	s := feedback.NewSender()
	// ghCmd returns exit code 4 (auth failure)
	s.GhCmd = func(args ...string) error {
		return &fakeExitError{code: 4}
	}
	// Force headless — only clipboard should fire, browser must NOT
	s.IsHeadlessFunc = func() bool { return true }
	s.ClipboardCmd = func(text string) error {
		clipboardCalled = true
		return nil
	}
	s.BrowserCmd = func(url string) error {
		browserCalled = true
		return nil
	}

	err := s.Send("1.5.1", 4, "darwin", "arm64", "")
	require.NoError(t, err)
	require.True(t, clipboardCalled, "clipboard must be called in headless mode")
	require.False(t, browserCalled, "browser must NOT be called in headless mode")
}

// TestUpgradeFeedback_ShownPerMajorVersion_RegressionFor967 pins the fix for
// issue #967: opting out of the upgrade feedback dialog at one major.minor
// release ("1.9.5") must NOT silence the dialog forever. When the user upgrades
// to the next release series ("1.10.0"), the dialog must show again so they get
// the chance to comment on the new version's prompt.
//
// Pre-#967 behavior: RecordOptOut flipped FeedbackEnabled=false permanently,
// gating every future ShouldShow call regardless of the version bump.
// Post-#967 behavior: RecordOptOut records the major.minor at which the user
// dismissed; ShouldShow only honors the opt-out while the current version is
// still on that same series.
func TestUpgradeFeedback_ShownPerMajorVersion_RegressionFor967(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Seed the state so pacing gates pass — the test is about version-scoped
	// opt-out, not pacing.
	st := oldShouldShowBypass(&feedback.State{
		LastRatedVersion: "",
		FeedbackEnabled:  true,
		ShownCount:       0,
		MaxShows:         3,
	})

	// Step 1: user is on v1.9.5 and dismisses the feedback prompt.
	feedback.RecordOptOut(st, "1.9.5")
	require.NoError(t, feedback.SaveState(st))

	loaded, err := feedback.LoadState()
	require.NoError(t, err)
	require.False(t, feedback.ShouldShow(loaded, "1.9.5", time.Now()),
		"on the same release series the opt-out must still suppress the dialog")

	// Step 2: agent-deck is upgraded to v1.10.0 (new release series).
	// The per-major-series opt-out should be cleared and the dialog should show.
	require.True(t, feedback.ShouldShow(loaded, "1.10.0", time.Now()),
		"after the release-series bump the opt-out must expire and the dialog must show again (#967)")
}

// TestUpgradeFeedback_LegacyForeverOptOut_MigratesGracefully pins the migration
// requirement from #967: existing state files that have FeedbackEnabled=false
// but no OptOutVersion (written before this fix) must be treated as
// "dismissed at the current release series" — i.e. they continue to suppress
// the dialog on the user's current version, but a future release-series bump
// re-shows it. They must NOT be treated as forever-silent.
//
// Production wiring runs MigrateLegacyOptOut from the TUI launch path
// (cmd/agent-deck/main.go) right after LoadState — this test mirrors that
// shape so the contract is pinned at the package boundary.
func TestUpgradeFeedback_LegacyForeverOptOut_MigratesGracefully(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Legacy state: FeedbackEnabled=false, no OptOutVersion. This is what a
	// pre-#967 user has on disk after clicking "no thanks" once.
	st := oldShouldShowBypass(&feedback.State{
		LastRatedVersion: "",
		FeedbackEnabled:  false,
		ShownCount:       0,
		MaxShows:         3,
	})
	require.NoError(t, feedback.SaveState(st))

	// First TUI launch on a binary that ships #967. The migration anchors
	// the opt-out at the currently-running release series.
	loaded, err := feedback.LoadState()
	require.NoError(t, err)
	require.True(t, feedback.MigrateLegacyOptOut(loaded, "1.9.5"),
		"first encounter with legacy state must migrate (and signal that state needs to be saved)")
	require.NoError(t, feedback.SaveState(loaded))

	// At the current version the migrated opt-out should still suppress the
	// dialog — users must not be re-prompted immediately after upgrading the
	// CLI itself.
	require.False(t, feedback.ShouldShow(loaded, "1.9.5", time.Now()),
		"migrated legacy opt-out must still suppress the dialog at the migration-anchor release series")

	// But a release-series bump must re-show it — this is the whole point of #967.
	require.True(t, feedback.ShouldShow(loaded, "1.10.0", time.Now()),
		"migrated legacy opt-out must expire on the next release-series bump (#967)")

	// Idempotence: re-running migration must be a no-op once OptOutVersion
	// is set — otherwise a later launch would silently re-anchor the opt-out
	// at a newer series and re-silence the user.
	require.False(t, feedback.MigrateLegacyOptOut(loaded, "1.10.0"),
		"migration must be idempotent once OptOutVersion is set")
}
