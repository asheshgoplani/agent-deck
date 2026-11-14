package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/issuereport"
)

// MINOR-7: once the send has started, Esc must not pretend to cancel (gh
// may already have filed the issue): the dialog stays on the sending step
// saying so, the result still arrives, and nothing is sent twice.
func TestReportEscWhileSendingDoesNotPretendToCancel(t *testing.T) {
	f := &reportFakes{res: issuereport.FileResult{Method: "gh", URL: "https://github.com/asheshgoplani/agent-deck/issues/4242"}}
	h := newReportTestHome(t, f)
	h.initialLoading = false
	fillBugForm(t, f, h)
	f.press(t, h, "ctrl+p")

	_, cmd := h.Update(reportTestKey("y")) // send started, result not yet delivered
	if h.reportDialog.step != reportStepSending {
		t.Fatalf("step = %v, want sending", h.reportDialog.step)
	}
	h.Update(reportTestKey("esc"))
	if !h.reportDialog.IsVisible() || h.reportDialog.step != reportStepSending {
		t.Fatalf("Esc while sending: visible=%v step=%v, want still sending", h.reportDialog.IsVisible(), h.reportDialog.step)
	}
	view := ansi.Strip(h.reportDialog.View())
	if !strings.Contains(view, reportSendingLine) || strings.Contains(view, "Esc cancels") {
		t.Fatalf("sending view must say it cannot be cancelled:\n%s", view)
	}

	runReportCmd(h, cmd)
	if f.calls != 1 {
		t.Fatalf("filer called %d times, want 1", f.calls)
	}
	if h.reportDialog.step != reportStepResult || h.reportDialog.ResultLine() != "Filed: https://github.com/asheshgoplani/agent-deck/issues/4242" {
		t.Fatalf("after Esc the result was lost: step=%v line=%q", h.reportDialog.step, h.reportDialog.ResultLine())
	}
	f.press(t, h, "esc")
	if h.reportDialog.IsVisible() {
		t.Fatal("Esc on the result did not close the dialog")
	}
	f.press(t, h, "y")
	if f.calls != 1 {
		t.Fatalf("filer called %d times after closing, want 1", f.calls)
	}
}

// MINOR-5: the preview says when the browser fallback will carry a trimmed
// body, and only then (gh not ready and the URL trimmed).
func TestReportPreviewSaysWhenBrowserTextIsTrimmed(t *testing.T) {
	big := strings.Repeat("log line with some words in it\n", 600)
	for _, tc := range []struct {
		name     string
		noGh     bool
		log      string
		wantNote bool
	}{
		{"browser and trimmed", true, big, true},
		{"gh ready", false, big, false},
		{"browser, short", true, "one line", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &reportFakes{noGh: tc.noGh, diag: issuereport.Diagnostics{Version: "1", LogExcerpt: tc.log}}
			h := newReportTestHome(t, f)
			fillBugForm(t, f, h)
			f.press(t, h, "ctrl+p")
			view := ansi.Strip(h.reportDialog.View())
			hasNote := strings.Contains(view, "TRIMMED")
			if hasNote != tc.wantNote {
				t.Fatalf("trimmed note shown=%v, want %v:\n%s", hasNote, tc.wantNote, view)
			}
			if tc.wantNote && strings.Contains(view, "this exact text will be filed") {
				t.Fatal("preview still promises the exact text although the browser gets less")
			}
		})
	}
}
