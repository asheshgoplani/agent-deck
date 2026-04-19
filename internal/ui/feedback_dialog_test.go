package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/feedback"
	tea "github.com/charmbracelet/bubbletea"
)

// fakeGhSender captures calls to Sender.GhCmd / BrowserCmd / ClipboardCmd so tests can
// assert that the TUI confirm flow (#679 follow-up for v1.7.37) never fires a gh post or
// a silent clipboard/browser fallback unless the user explicitly consented with 'y'.
type fakeGhSender struct {
	ghCalls      int
	ghArgs       [][]string
	ghErr        error
	browserCalls int
	clipCalls    int
}

func (f *fakeGhSender) gh(args ...string) error {
	f.ghCalls++
	// copy to avoid aliasing with caller's slice
	a := make([]string, len(args))
	copy(a, args)
	f.ghArgs = append(f.ghArgs, a)
	return f.ghErr
}
func (f *fakeGhSender) browser(_ string) error { f.browserCalls++; return nil }
func (f *fakeGhSender) clip(_ string) error    { f.clipCalls++; return nil }

func newDialogWithFakeSender(t *testing.T, version string) (*FeedbackDialog, *fakeGhSender) {
	t.Helper()
	fs := &fakeGhSender{}
	sender := &feedback.Sender{
		GhCmd:          fs.gh,
		BrowserCmd:     fs.browser,
		ClipboardCmd:   fs.clip,
		IsHeadlessFunc: func() bool { return true },
	}
	d := NewFeedbackDialog()
	d.Show(version, &feedback.State{FeedbackEnabled: true, MaxShows: 3}, sender)
	return d, fs
}

func dialogAtStepConfirm(t *testing.T, version, comment string) (*FeedbackDialog, *fakeGhSender) {
	t.Helper()
	d, fs := newDialogWithFakeSender(t, version)
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if d.step != stepComment {
		t.Fatalf("setup: expected stepComment after rating key, got %d", d.step)
	}
	d.commentInput.SetValue(comment)
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if d.step != stepConfirm {
		t.Fatalf("setup: expected stepConfirm after Enter, got %d", d.step)
	}
	return d, fs
}

// TestFeedbackDialog_InitiallyHidden verifies NewFeedbackDialog() returns a dialog that is not visible.
func TestFeedbackDialog_InitiallyHidden(t *testing.T) {
	d := NewFeedbackDialog()
	if d == nil {
		t.Fatal("NewFeedbackDialog() returned nil")
	}
	if d.IsVisible() {
		t.Error("expected dialog to be hidden initially, but IsVisible() returned true")
	}
}

// TestFeedbackDialog_ShowMakesVisible verifies Show() makes the dialog visible.
func TestFeedbackDialog_ShowMakesVisible(t *testing.T) {
	d := NewFeedbackDialog()
	st := &feedback.State{FeedbackEnabled: true, MaxShows: 3, ShownCount: 1}
	sender := feedback.NewSender()
	d.Show("1.5.1", st, sender)
	if !d.IsVisible() {
		t.Error("expected dialog to be visible after Show(), but IsVisible() returned false")
	}
}

// TestFeedbackDialog_RatingKey_AdvancesToComment verifies pressing '3' at stepRating transitions to stepComment.
func TestFeedbackDialog_RatingKey_AdvancesToComment(t *testing.T) {
	d := NewFeedbackDialog()
	st := &feedback.State{FeedbackEnabled: true, MaxShows: 3, ShownCount: 1}
	sender := feedback.NewSender()
	d.Show("1.5.1", st, sender)

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if d.step != stepComment {
		t.Errorf("expected step to be stepComment (%d), got %d", stepComment, d.step)
	}
	if d.rating != 3 {
		t.Errorf("expected rating to be 3, got %d", d.rating)
	}
}

// TestFeedbackDialog_AllRatingKeys verifies keys '1'-'5' store the correct integer rating.
func TestFeedbackDialog_AllRatingKeys(t *testing.T) {
	cases := []struct {
		key    rune
		rating int
	}{
		{'1', 1},
		{'2', 2},
		{'3', 3},
		{'4', 4},
		{'5', 5},
	}
	for _, tc := range cases {
		d := NewFeedbackDialog()
		st := &feedback.State{FeedbackEnabled: true, MaxShows: 3, ShownCount: 1}
		sender := feedback.NewSender()
		d.Show("1.5.1", st, sender)

		d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tc.key}})
		if d.rating != tc.rating {
			t.Errorf("key '%c': expected rating %d, got %d", tc.key, tc.rating, d.rating)
		}
		if d.step != stepComment {
			t.Errorf("key '%c': expected stepComment (%d), got %d", tc.key, stepComment, d.step)
		}
	}
}

// TestFeedbackDialog_OptOutKey verifies pressing 'n' at stepRating hides dialog and records opt-out.
func TestFeedbackDialog_OptOutKey(t *testing.T) {
	d := NewFeedbackDialog()
	st := &feedback.State{FeedbackEnabled: true, MaxShows: 3, ShownCount: 1}
	sender := feedback.NewSender()
	d.Show("1.5.1", st, sender)

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if d.IsVisible() {
		t.Error("expected dialog to be hidden after opt-out, but IsVisible() returned true")
	}
	if st.FeedbackEnabled {
		t.Error("expected FeedbackEnabled to be false after opt-out, but it is still true")
	}
}

// TestFeedbackDialog_EscAtRating_HidesWithoutOptOut verifies Esc at stepRating hides without opt-out.
func TestFeedbackDialog_EscAtRating_HidesWithoutOptOut(t *testing.T) {
	d := NewFeedbackDialog()
	st := &feedback.State{FeedbackEnabled: true, MaxShows: 3, ShownCount: 1}
	sender := feedback.NewSender()
	d.Show("1.5.1", st, sender)

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if d.IsVisible() {
		t.Error("expected dialog to be hidden after Esc, but IsVisible() returned true")
	}
	if !st.FeedbackEnabled {
		t.Error("expected FeedbackEnabled to remain true after Esc, but it was set to false")
	}
}

// TestFeedbackDialog_EnterAtComment_TransitionsToConfirm verifies that Enter at stepComment
// no longer posts silently — it must route through the new stepConfirm disclosure (v1.7.37,
// closing the TUI side of the #679 privacy gap).
func TestFeedbackDialog_EnterAtComment_TransitionsToConfirm(t *testing.T) {
	d, fs := newDialogWithFakeSender(t, "1.7.37")
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if d.step != stepComment {
		t.Fatalf("expected stepComment after rating key, got %d", d.step)
	}

	d.commentInput.SetValue("scrollback fix")
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if d.step != stepConfirm {
		t.Errorf("expected stepConfirm after Enter at stepComment, got %d", d.step)
	}
	if fs.ghCalls != 0 {
		t.Errorf("expected zero gh calls before user confirms, got %d", fs.ghCalls)
	}
	if fs.browserCalls != 0 || fs.clipCalls != 0 {
		t.Errorf("expected no browser/clipboard side-effects before confirm, got browser=%d clip=%d",
			fs.browserCalls, fs.clipCalls)
	}
}

// TestFeedbackDialog_Confirm_N_DismissesWithoutSend verifies pressing 'n' at stepConfirm
// routes to stepDismissed and never touches gh/browser/clipboard.
func TestFeedbackDialog_Confirm_N_DismissesWithoutSend(t *testing.T) {
	d, fs := dialogAtStepConfirm(t, "1.7.37", "hi")
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if d.step != stepDismissed {
		t.Errorf("expected stepDismissed after 'n', got %d", d.step)
	}
	if fs.ghCalls != 0 {
		t.Errorf("expected no gh calls on decline, got %d", fs.ghCalls)
	}
	if fs.browserCalls != 0 || fs.clipCalls != 0 {
		t.Errorf("expected no silent fallback, got browser=%d clip=%d", fs.browserCalls, fs.clipCalls)
	}
}

// TestFeedbackDialog_Confirm_Esc_DismissesWithoutSend verifies Esc at stepConfirm cancels cleanly.
func TestFeedbackDialog_Confirm_Esc_DismissesWithoutSend(t *testing.T) {
	d, fs := dialogAtStepConfirm(t, "1.7.37", "hi")
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if d.step != stepDismissed {
		t.Errorf("expected stepDismissed after Esc, got %d", d.step)
	}
	if fs.ghCalls != 0 {
		t.Errorf("expected no gh calls on Esc, got %d", fs.ghCalls)
	}
}

// TestFeedbackDialog_Confirm_Y_TransitionsToSent verifies 'y' advances to stepSent and
// that executing the returned tea.Cmd's sendCmd hits gh (directly, no fallback).
func TestFeedbackDialog_Confirm_Y_TransitionsToSent(t *testing.T) {
	d, fs := dialogAtStepConfirm(t, "1.7.37", "scrollback fix")
	d, cmd := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if d.step != stepSent {
		t.Errorf("expected stepSent after 'y', got %d", d.step)
	}
	if cmd == nil {
		t.Fatal("expected non-nil tea.Cmd from y-confirm")
	}
	// Directly exercise the dialog's sendCmd (bypassing tea.Batch plumbing) to confirm
	// it uses GhCmd directly and not the 3-tier fallback.
	msg := d.sendCmd(d.commentInput.Value())()
	if fs.ghCalls == 0 {
		t.Error("expected gh to be called after y-confirm")
	}
	if fs.browserCalls != 0 || fs.clipCalls != 0 {
		t.Errorf("expected NO silent fallback on success path, got browser=%d clip=%d",
			fs.browserCalls, fs.clipCalls)
	}
	sm, ok := msg.(feedbackSentMsg)
	if !ok {
		t.Fatalf("expected feedbackSentMsg, got %T", msg)
	}
	if sm.err != nil {
		t.Errorf("expected nil err on gh success, got %v", sm.err)
	}
}

// TestFeedbackDialog_SendCmd_NoSilentFallback_OnGhError verifies gh errors surface as
// feedbackSentMsg{err:...} and do NOT trigger browser/clipboard fallback (v1.7.37).
func TestFeedbackDialog_SendCmd_NoSilentFallback_OnGhError(t *testing.T) {
	d, fs := dialogAtStepConfirm(t, "1.7.37", "anything")
	fs.ghErr = errors.New("gh: not authenticated")

	msg := d.sendCmd("anything")()

	sm, ok := msg.(feedbackSentMsg)
	if !ok {
		t.Fatalf("expected feedbackSentMsg, got %T", msg)
	}
	if sm.err == nil {
		t.Error("expected gh error to surface, got nil (silent failure would be a regression)")
	}
	if fs.browserCalls != 0 || fs.clipCalls != 0 {
		t.Errorf("expected NO silent fallback on gh error, got browser=%d clip=%d",
			fs.browserCalls, fs.clipCalls)
	}
}

// TestFeedbackDialog_ConfirmView_ContainsDisclosure verifies View() at stepConfirm shows
// the URL, the "PUBLICLY" warning, the authenticated gh login (with @-prefix), and the
// exact body that will be posted.
func TestFeedbackDialog_ConfirmView_ContainsDisclosure(t *testing.T) {
	orig := ghUserLogin
	ghUserLogin = func() string { return "testuser" }
	defer func() { ghUserLogin = orig }()

	d, _ := dialogAtStepConfirm(t, "1.7.37", "scrollback fix")
	v := d.View()

	mustContain := []string{
		"PUBLICLY",
		"https://github.com/asheshgoplani/agent-deck/discussions/600",
		"@testuser",
		"scrollback fix",
		"1.7.37",
	}
	for _, s := range mustContain {
		if !strings.Contains(v, s) {
			t.Errorf("stepConfirm view missing %q\nFull view:\n%s", s, v)
		}
	}
}

// TestFeedbackDialog_ConfirmView_FallsBackWhenGhLoginEmpty verifies the view degrades
// gracefully when gh is unauthenticated or unavailable (no "@<login>" rendered).
func TestFeedbackDialog_ConfirmView_FallsBackWhenGhLoginEmpty(t *testing.T) {
	orig := ghUserLogin
	ghUserLogin = func() string { return "" }
	defer func() { ghUserLogin = orig }()

	d, _ := dialogAtStepConfirm(t, "1.7.37", "bug")
	v := d.View()

	if strings.Contains(v, "@") && !strings.Contains(v, "@asheshgoplani") {
		// Allowed: URL contains "asheshgoplani" but no user-@-handle should render.
		if strings.Contains(v, "As:     @") {
			t.Errorf("expected no @<login> when ghUserLogin empty, got:\n%s", v)
		}
	}
	if !strings.Contains(v, "your GitHub account") {
		t.Errorf("expected generic 'your GitHub account' fallback, got:\n%s", v)
	}
}

// TestFeedbackDialog_OnSent_ErrorRendersInSentView verifies that when gh fails, the
// stepSent view shows an explicit error message instead of the "Sent! Thanks..." text.
func TestFeedbackDialog_OnSent_ErrorRendersInSentView(t *testing.T) {
	d, _ := dialogAtStepConfirm(t, "1.7.37", "x")
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	d.OnSent(feedbackSentMsg{err: errors.New("gh api: authentication required")})

	v := d.View()
	lv := strings.ToLower(v)
	if !strings.Contains(lv, "error") {
		t.Errorf("expected 'error' in stepSent view on gh failure, got:\n%s", v)
	}
	if !strings.Contains(lv, "not sent") && !strings.Contains(lv, "not posted") {
		t.Errorf("expected 'not sent'/'not posted' phrasing on gh failure, got:\n%s", v)
	}
	if strings.Contains(lv, "thanks for the feedback") {
		t.Errorf("expected no success text on gh failure, got:\n%s", v)
	}
}

// TestFeedbackDialog_OnSent_SuccessRendersPostedMessage verifies that the success view
// mentions the Discussion number so users know where it landed.
func TestFeedbackDialog_OnSent_SuccessRendersPostedMessage(t *testing.T) {
	d, _ := dialogAtStepConfirm(t, "1.7.37", "x")
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	d.OnSent(feedbackSentMsg{err: nil})

	v := d.View()
	if !strings.Contains(v, "#600") && !strings.Contains(v, "Posted") {
		t.Errorf("expected 'Posted' or '#600' on success view, got:\n%s", v)
	}
}

// TestFeedbackDialog_ViewNonEmpty verifies View() returns non-empty string when visible, empty when hidden.
func TestFeedbackDialog_ViewNonEmpty(t *testing.T) {
	d := NewFeedbackDialog()
	st := &feedback.State{FeedbackEnabled: true, MaxShows: 3, ShownCount: 1}
	sender := feedback.NewSender()

	// Hidden: should return empty string
	if v := d.View(); v != "" {
		t.Errorf("expected empty View() when hidden, got %q", v)
	}

	// Visible: should return non-empty string
	d.Show("1.5.1", st, sender)
	if v := d.View(); v == "" {
		t.Error("expected non-empty View() when visible, got empty string")
	}
}

// TestFeedbackDialog_OnDemandShortcut verifies that Show() makes the dialog visible regardless
// of opt-out state or prior rating -- i.e. on-demand bypasses ShouldShow() entirely.
// This mirrors what the ctrl+e handler does: call Show() unconditionally.
func TestFeedbackDialog_OnDemandShortcut(t *testing.T) {
	sender := feedback.NewSender()

	// Case 1: LastRatedVersion matches current version (auto-popup would block this).
	d1 := NewFeedbackDialog()
	st1 := &feedback.State{
		FeedbackEnabled:  true,
		LastRatedVersion: "1.5.1", // already rated this version
		MaxShows:         3,
		ShownCount:       1,
	}
	d1.Show("1.5.1", st1, sender)
	if !d1.IsVisible() {
		t.Error("case 1: expected dialog to be visible after on-demand Show() even though LastRatedVersion matches, but IsVisible() returned false")
	}

	// Case 2: FeedbackEnabled=false (auto-popup would block this).
	d2 := NewFeedbackDialog()
	d2.Show("1.5.1", &feedback.State{FeedbackEnabled: true, MaxShows: 3}, sender)
	d2.Hide()
	if d2.IsVisible() {
		t.Fatal("expected dialog hidden after Hide(), but IsVisible() returned true")
	}

	st2 := &feedback.State{
		FeedbackEnabled: false, // user opted out -- auto-popup would skip entirely
		MaxShows:        3,
	}
	d2.Show("1.5.1", st2, sender)
	if !d2.IsVisible() {
		t.Error("case 2: expected dialog to be visible after on-demand Show() even with FeedbackEnabled=false, but IsVisible() returned false")
	}
}
