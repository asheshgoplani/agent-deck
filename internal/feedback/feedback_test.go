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
	feedback.RecordOptOut(st)
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
