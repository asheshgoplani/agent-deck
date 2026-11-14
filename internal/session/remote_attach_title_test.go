package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// Review round (title notices): the attach's notices went into the terminal
// title and nothing put the owner's title back. A failure or the update
// notice stayed there after the attach, and a success left the title blank.

// syncBuffer is a strings.Builder safe for the notice timer's goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) take() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.b.String()
	s.b.Reset()
	return out
}

func TestAttachOutputNoticeSavesTheTitleOnceAndRestoresIt(t *testing.T) {
	if titlePush != "\x1b[22;2t" || titlePop != "\x1b]2;\x07\x1b[23;2t" {
		t.Fatalf("save with XTWINOPS 22;2, restore with an empty title then 23;2: %q %q", titlePush, titlePop)
	}
	buf := &syncBuffer{}
	o := &attachOutput{w: buf}
	o.progress("one")
	if got := buf.take(); got != titlePush+"\x1b]2;one\x07" {
		t.Fatalf("the first notice saves the title, then sets it: %q", got)
	}
	o.progress("two")
	if got := buf.take(); got != "\x1b]2;two\x07" {
		t.Fatalf("a second notice does not save again (one push, one pop): %q", got)
	}
	o.notice("")
	if got := buf.take(); got != titlePop {
		t.Fatalf("clearing restores the saved title instead of blanking it: %q", got)
	}
	o.notice("")
	if got := buf.take(); got != "" {
		t.Fatalf("nothing saved, nothing to pop: %q", got)
	}
}

func TestAttachOutputNoticeExpiresButProgressStays(t *testing.T) {
	buf := &syncBuffer{}
	o := &attachOutput{w: buf, hold: 30 * time.Millisecond}
	o.notice("agent-deck v9 is installed: detach to restart")
	_ = buf.take()
	waitForTitleRestore(t, buf, "a notice expires")
	o.progress("uploading")
	_ = buf.take()
	time.Sleep(100 * time.Millisecond)
	if got := buf.take(); got != "" {
		t.Fatalf("an uploading line stays until its result replaces it: %q", got)
	}
	// A newer notice is not cut short by an older one's timer.
	o.alert("a")
	time.Sleep(20 * time.Millisecond)
	o.alert("b")
	time.Sleep(15 * time.Millisecond)
	if got := buf.take(); strings.Contains(got, "\x1b[23;2t") {
		t.Fatalf("the first notice's timer restored over the second: %q", got)
	}
	waitForTitleRestore(t, buf, "the second notice expires")
}

func waitForTitleRestore(t *testing.T, buf *syncBuffer, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.take(), "\x1b[23;2t") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s: the title was never restored", what)
}

func TestAttachOutputAlertRingsTheBellNoticeDoesNot(t *testing.T) {
	buf := &syncBuffer{}
	o := &attachOutput{w: buf}
	o.notice("agent-deck v9 is installed: detach to restart")
	if got := buf.take(); strings.HasSuffix(got, "\x07\a") {
		t.Fatalf("an update notice does not ring: %q", got)
	}
	o.progress(pasteUploadingNotice("deckbox"))
	if got := buf.take(); strings.HasSuffix(got, "\x07\a") {
		t.Fatalf("progress does not ring: %q", got)
	}
	o.alert(pasteTooLargeNotice())
	if got := buf.take(); !strings.HasSuffix(got, "\x07\a") {
		t.Fatalf("an alert rings after the title: %q", got)
	}
}

// Every way the attach ends goes through stopRemoteAttach, which restores the
// title; notices after that do nothing, and a timer never pops twice.
func TestStopRemoteAttachRestoresTheTitle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport string
		exited    bool
	}{
		{"ssh detach", RemoteTransportSSH, false},
		{"ssh exited", RemoteTransportSSH, true},
		{"mosh exited", RemoteTransportMosh, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			outputDone := make(chan struct{})
			close(outputDone)
			buf := &syncBuffer{}
			out := &attachOutput{w: buf, hold: 20 * time.Millisecond}
			out.alert("agent-deck: the image could not reach host: boom")
			_ = buf.take()
			stopRemoteAttach(&exec.Cmd{}, r, make(chan error, 1), outputDone, out, tc.transport, tc.exited, time.Second)
			if got := buf.take(); got != titlePop {
				t.Fatalf("the attach ended without restoring the title: %q", got)
			}
			out.notice("late")
			out.endNotices()
			time.Sleep(60 * time.Millisecond)
			if got := buf.take(); got != "" {
				t.Fatalf("after the attach: %q", got)
			}
		})
	}
	t.Run("mosh handoff", func(t *testing.T) {
		cmd, ptmx, cmdDone, outputDone := startAttachFixture(t, `trap 'exit 0' TERM; while :; do sleep 0.05; done`)
		buf := &syncBuffer{}
		out := &attachOutput{w: buf}
		out.notice("agent-deck v9 is installed: detach to restart")
		_ = buf.take()
		if !stopRemoteAttach(cmd, ptmx, cmdDone, outputDone, out, RemoteTransportMosh, false, time.Second) {
			t.Fatal("mosh detach did not hand off")
		}
		if got := buf.take(); got != titlePop {
			t.Fatalf("the handoff did not restore the title: %q", got)
		}
	})
	t.Run("no notice", func(t *testing.T) {
		buf := &syncBuffer{}
		out := &attachOutput{w: buf}
		out.endNotices()
		out.notice("after the end")
		if got := buf.take(); got != "" {
			t.Fatalf("no notice was shown, so the title is not touched, and none after the end: %q", got)
		}
	})
}

// The too-large case is an alert (bell), so it is noticed where the title is
// not shown (a local tmux pane, a terminal that hides titles).
func TestRemoteImagePaste_TooLargeAndFailuresAreAlerts(t *testing.T) {
	rec := &pasteRecorder{}
	p := newTestPaste(rec, nil, nil, "claude", false)
	p.readImage = func() clipboardRead { return clipboardRead{tooLarge: true} }
	input := sshAttachInput{writer: rec, paste: p}
	_, _ = input.forward([]byte{0x16})
	if alerts := rec.alertsSnapshot(); len(alerts) != 1 || alerts[0] != pasteTooLargeNotice() {
		t.Fatalf("too large: %q", alerts)
	}

	rec = &pasteRecorder{}
	input = sshAttachInput{writer: rec, paste: newTestPaste(rec, pngBytes, errors.New("boom"), "claude", false)}
	_, _ = input.forward([]byte{0x16})
	waitFor(t, func() bool { return len(rec.alertsSnapshot()) == 1 })
	if alerts := rec.alertsSnapshot(); len(alerts) != 1 || alerts[0] != pasteFailedNotice("deckbox", errors.New("boom")) {
		t.Fatalf("failure: %q", alerts)
	}
}

// Review LOW: a clipboard read that ran out of time (osascript hex-encoding
// a large image past 1.5 s) was taken for "no image" and Ctrl+V went on
// silently. It is reported now, like an image over the cap.
func TestBoundedOutputReportsATimeout(t *testing.T) {
	data, status := boundedOutput(100*time.Millisecond, 1000, "sh", "-c", "head -c 10 /dev/zero; exec sleep 5")
	if status != clipTimedOut || data != nil {
		t.Fatalf("a reader past its deadline: status %v, %d bytes", status, len(data))
	}
	if _, status := boundedOutput(time.Second, 1000, "sh", "-c", "exit 1"); status != clipNone {
		t.Fatalf("a reader that fails fast is no image: %v", status)
	}
}

func TestRemoteImagePaste_ClipboardTimeoutSaysSoOnlyOverTheComposer(t *testing.T) {
	for _, tc := range []struct {
		pane      string
		wantAlert bool
	}{{claudeComposerPane, true}, {vimPane, false}} {
		rec := &pasteRecorder{}
		p := newTestPasteOn(rec, nil, nil, "claude", tc.pane, nil, false)
		p.readImage = func() clipboardRead { return clipboardRead{timedOut: true} }
		input := sshAttachInput{writer: rec, paste: p}
		_, _ = input.forward([]byte{0x16})
		typed, _, uploads := rec.snapshot()
		alerts := rec.alertsSnapshot()
		if tc.wantAlert && (typed != "" || uploads != 0 || len(alerts) != 1 || alerts[0] != pasteClipboardSlowNotice()) {
			t.Fatalf("over the composer: the slow-clipboard line, nothing sent: %q %d %q", typed, uploads, alerts)
		}
		if !tc.wantAlert && (typed != "\x16" || len(alerts) != 0) {
			t.Fatalf("in vim the key goes on as typed: %q %q", typed, alerts)
		}
	}
	h := newSSHPasteHint(func(string) string { return "1.2.3.4 1 5.6.7.8 22" },
		func() clipboardRead { return clipboardRead{timedOut: true} }, "claude",
		func() string { return claudeComposerPane }, func(string) { t.Error("the hint showed over a clipboard that timed out") })
	h.check()
}

// Review LOW: a host path with a space (a home such as /Users/John Smith)
// failed with a generic error. The error now says what is wrong and why.
func TestSSHRunnerUploadImageSaysWhyAPathWithASpaceIsRefused(t *testing.T) {
	r := NewSSHRunner("deckbox", RemoteConfig{Host: "u@box"})
	answer, _ := json.Marshal(map[string]string{"path": "/Users/John Smith/.agent-deck/macapp-uploads/s1/a.png"})
	r.runStdinFn = func(context.Context, []byte, ...string) ([]byte, error) { return answer, nil }
	_, err := r.UploadImage(context.Background(), "s1", pngBytes, "png")
	if err == nil || !strings.Contains(err.Error(), "/Users/John Smith") || !strings.Contains(err.Error(), "space") || !strings.Contains(err.Error(), "@mention") {
		t.Fatalf("the error names the path and the cause: %v", err)
	}
	answer, _ = json.Marshal(map[string]string{"path": "/p/a\x1b[2Jb.png"})
	_, err = r.UploadImage(context.Background(), "s1", pngBytes, "png")
	if err == nil || strings.Contains(err.Error(), "\x1b") || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("a control character is named, never echoed raw: %v", err)
	}
}
