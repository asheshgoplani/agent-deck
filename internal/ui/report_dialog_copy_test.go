package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Review LOW (copy can claim false success): with no browser here (SSH or
// headless), a native tool like xclip may write to the remote host's X
// clipboard, so the copy must try OSC 52 (the user's own terminal) first.
// A native tool that hangs must not leave the note on "Copying…".
func TestReportCopy_PrefersOSC52WithoutBrowserAndTimesOut(t *testing.T) {
	type calls struct{ osc, native int }
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	newCopier := func(c *calls, oscErr error, nativeHangs bool) reportCopier {
		return reportCopier{
			osc52Supported: true,
			timeout:        50 * time.Millisecond,
			osc52: func(string) error {
				c.osc++
				return oscErr
			},
			native: func(ctx context.Context, _ string, osc bool) (string, error) {
				c.native++
				if osc && oscErr != nil {
					t.Error("native copy retried OSC 52 after it already failed")
				}
				if nativeHangs {
					<-release // ignores ctx on purpose
				}
				return "xclip", nil
			},
		}
	}

	t.Run("no browser: OSC 52 first, native untouched", func(t *testing.T) {
		var c calls
		method, err := newCopier(&c, nil, false).copy("u", false)
		if err != nil || method != "osc52" || c.osc != 1 || c.native != 0 {
			t.Fatalf("method=%q err=%v calls=%+v, want osc52 only", method, err, c)
		}
	})
	t.Run("no browser, OSC 52 fails: falls back to native", func(t *testing.T) {
		var c calls
		method, err := newCopier(&c, errors.New("no tty"), false).copy("u", false)
		if err != nil || method != "xclip" || c.osc != 1 || c.native != 1 {
			t.Fatalf("method=%q err=%v calls=%+v, want osc52 then xclip", method, err, c)
		}
	})
	t.Run("browser here: native first", func(t *testing.T) {
		var c calls
		method, err := newCopier(&c, nil, false).copy("u", true)
		if err != nil || method != "xclip" || c.osc != 0 || c.native != 1 {
			t.Fatalf("method=%q err=%v calls=%+v, want native only", method, err, c)
		}
	})
	t.Run("native tool hangs: times out", func(t *testing.T) {
		var c calls
		done := make(chan error, 1)
		go func() { _, err := newCopier(&c, nil, true).copy("u", true); done <- err }()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "timed out") {
				t.Fatalf("err = %v, want a timeout", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("copy hung on a stuck clipboard tool")
		}
	})
}

// An OSC 52 write cannot be confirmed, so the note must not claim the
// clipboard holds the URL for sure.
func TestReportCopy_OSC52NoteIsHonest(t *testing.T) {
	d := NewReportDialog()
	d.visible, d.step, d.seq = true, reportStepResult, 7
	d.OnCopied(reportCopiedMsg{seq: 7, method: "osc52"})
	if strings.Contains(d.copyNote, "Copied the URL to the clipboard") || !strings.Contains(d.copyNote, "OSC 52") {
		t.Fatalf("OSC 52 note = %q", d.copyNote)
	}
	d.OnCopied(reportCopiedMsg{seq: 7, method: "xclip"})
	if d.copyNote != "Copied the URL to the clipboard." {
		t.Fatalf("native note = %q", d.copyNote)
	}
}
