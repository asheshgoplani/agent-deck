package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/issuereport"
)

// reportBoxText joins what is inside the dialog's border, line by line with
// no separators, so a URL wrapped over several rows reads as one string.
func reportBoxText(view string) string {
	var b strings.Builder
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "│")
		line = strings.TrimSuffix(line, "│")
		b.WriteString(strings.TrimSpace(line))
	}
	return b.String()
}

// Review MEDIUM: with gh not logged in on a headless/SSH host no browser can
// open. The result must show the prefilled URL itself (and offer to copy it)
// instead of hiding it behind "run feedback --dry-run", which makes the user
// type the report again.
func TestReportNoBrowserShowsPrefilledURL(t *testing.T) {
	url := "https://github.com/asheshgoplani/agent-deck/issues/new?labels=bug%2Ctriage&title=letters+doubled&body=" +
		strings.Repeat("abcdefghij", 30) + "END"
	for _, tc := range []struct {
		name     string
		headless bool
		res      issuereport.FileResult
		err      error
	}{
		{"headless, browser not tried", true, issuereport.FileResult{Method: "browser", URL: url}, nil},
		{"browser failed to open", false, issuereport.FileResult{}, &issuereport.Error{Code: issuereport.ErrBrowserFailed,
			Message: "could not open a browser; open this URL yourself: " + url, URL: url, Err: errors.New("exit status 3")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &reportFakes{noGh: true, headless: tc.headless, res: tc.res, err: tc.err}
			h := newReportTestHome(t, f)
			var copied []string
			var copiedBrowserHere []bool
			h.reportDialog.copyText = func(s string, browserHere bool) (string, error) {
				copied = append(copied, s)
				copiedBrowserHere = append(copiedBrowserHere, browserHere)
				return "test", nil
			}
			fillBugForm(t, f, h)
			f.press(t, h, "ctrl+p")
			if pv := ansi.Strip(h.reportDialog.View()); tc.headless != strings.Contains(pv, "no browser on this machine") {
				t.Fatalf("headless=%v but the preview's How line disagrees:\n%s", tc.headless, pv)
			}
			f.press(t, h, "y")
			d := h.reportDialog
			if d.step != reportStepResult {
				t.Fatalf("step = %v, want result", d.step)
			}
			line := d.ResultLine()
			if strings.Contains(line, "Opened in your browser") || strings.Contains(line, "--dry-run") {
				t.Fatalf("result line hides the URL or claims a browser opened: %q", line)
			}
			view := d.View()
			if !strings.Contains(reportBoxText(view), url) {
				t.Fatalf("the full prefilled URL is not on screen:\n%s", ansi.Strip(view))
			}
			if !strings.Contains(ansi.Strip(view), "c: copy URL") {
				t.Fatalf("no copy key offered:\n%s", ansi.Strip(view))
			}
			_, cmd := h.Update(reportTestKey("c"))
			for cmd != nil {
				msg := cmd()
				if _, ok := msg.(reportCopiedMsg); !ok {
					break
				}
				_, cmd = h.Update(msg)
			}
			if len(copied) != 1 || copied[0] != url {
				t.Fatalf("copied %q, want the prefilled URL", copied)
			}
			if copiedBrowserHere[0] != !tc.headless {
				t.Fatalf("copy told browserHere=%v, headless=%v", copiedBrowserHere[0], tc.headless)
			}
			if !strings.Contains(ansi.Strip(d.View()), "Copied") {
				t.Fatalf("no copy confirmation:\n%s", ansi.Strip(d.View()))
			}
			if !d.IsVisible() || f.calls != 1 {
				t.Fatalf("copy closed the dialog or re-sent: visible=%v calls=%d", d.IsVisible(), f.calls)
			}
		})
	}
}

// When the browser did open, the dialog keeps the short "press Submit" line
// and offers no copy key.
func TestReportBrowserOpenedNoCopyKey(t *testing.T) {
	f := &reportFakes{noGh: true, res: issuereport.FileResult{Method: "browser", URL: "https://github.com/asheshgoplani/agent-deck/issues/new?title=x", Opened: true}}
	h := newReportTestHome(t, f)
	fillBugForm(t, f, h)
	f.press(t, h, "ctrl+p")
	f.press(t, h, "y")
	if v := ansi.Strip(h.reportDialog.View()); strings.Contains(v, "c: copy URL") {
		t.Fatalf("copy key offered although the browser opened:\n%s", v)
	}
}
