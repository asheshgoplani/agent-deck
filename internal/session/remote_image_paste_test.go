package session

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Owner report 2026-10-18: in a terminal attached to a remote session, Ctrl+V
// with a screenshot on the Mac did nothing (Claude on the host reads the
// host's clipboard). The attach now sends the image up and types its host
// path. These tests drive sshAttachInput, the attach's own input path.

var pngBytes = []byte("\x89PNG\r\n\x1a\n" + "rest-of-a-picture")

type pasteRecorder struct {
	mu      sync.Mutex
	typed   bytes.Buffer
	notices []string
	uploads [][]byte
	exts    []string
}

func (r *pasteRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.typed.Write(p)
}

func (r *pasteRecorder) snapshot() (string, []string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.typed.String(), append([]string(nil), r.notices...), len(r.uploads)
}

func newTestPaste(rec *pasteRecorder, image []byte, uploadErr error, tool string, bracketed bool) *remoteImagePaste {
	return &remoteImagePaste{
		remote:    "deckbox",
		readImage: func() ([]byte, string) { return image, "png" },
		upload: func(_ context.Context, data []byte, ext string) (string, error) {
			rec.mu.Lock()
			rec.uploads = append(rec.uploads, data)
			rec.exts = append(rec.exts, ext)
			rec.mu.Unlock()
			if uploadErr != nil {
				return "", uploadErr
			}
			return "/home/u/.agent-deck/profiles/default/macapp-uploads/s1/A.png", nil
		},
		tool:      func(context.Context) string { return tool },
		write:     func(p []byte) error { _, err := rec.Write(p); return err },
		notice:    func(text string) { rec.mu.Lock(); rec.notices = append(rec.notices, text); rec.mu.Unlock() },
		bracketed: func() bool { return bracketed },
	}
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !done() {
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRemoteImagePaste_CtrlVWithAnImageUploadsAndTypesTheHostPath(t *testing.T) {
	for _, bracketed := range []bool{false, true} {
		rec := &pasteRecorder{}
		input := sshAttachInput{writer: rec, paste: newTestPaste(rec, pngBytes, nil, "claude", bracketed)}
		detached, err := input.forward([]byte{0x16})
		if detached || err != nil {
			t.Fatalf("Ctrl+V is not a detach: %v %v", detached, err)
		}
		want := "@/home/u/.agent-deck/profiles/default/macapp-uploads/s1/A.png "
		if bracketed {
			want = "\x1b[200~" + want + "\x1b[201~"
		}
		waitFor(t, func() bool { typed, _, _ := rec.snapshot(); return typed != "" })
		typed, notices, uploads := rec.snapshot()
		if typed != want {
			t.Fatalf("bracketed=%v: typed %q, want %q (the raw Ctrl+V must never reach the host)", bracketed, typed, want)
		}
		if uploads != 1 || !bytes.Equal(rec.uploads[0], pngBytes) || rec.exts[0] != "png" {
			t.Fatalf("the clipboard's PNG goes up once: %d uploads", uploads)
		}
		if len(notices) != 2 || notices[0] != pasteUploadingNotice("deckbox") || notices[1] != "" {
			t.Fatalf("an uploading line, cleared when the path is typed: %q", notices)
		}
	}
}

func TestRemoteImagePaste_NoImageLeavesCtrlVToTheHost(t *testing.T) {
	rec := &pasteRecorder{}
	input := sshAttachInput{writer: rec, paste: newTestPaste(rec, nil, nil, "claude", false)}
	if _, err := input.forward([]byte{0x16}); err != nil {
		t.Fatal(err)
	}
	if typed, notices, uploads := rec.snapshot(); typed != "\x16" || uploads != 0 || len(notices) != 0 {
		t.Fatalf("no image: Ctrl+V goes through as before: typed %q, %d uploads, notices %q", typed, uploads, notices)
	}
}

func TestRemoteImagePaste_TextAndOtherKeysPassUnchanged(t *testing.T) {
	rec := &pasteRecorder{}
	input := sshAttachInput{writer: rec, paste: newTestPaste(rec, pngBytes, nil, "claude", false)}
	for _, chunk := range []string{"hello", "\x1b[200~pasted text\x16with ctrl-v inside\x1b[201~", "\x16\x16"} {
		if _, err := input.forward([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	typed, _, uploads := rec.snapshot()
	if typed != "hello\x1b[200~pasted text\x16with ctrl-v inside\x1b[201~\x16\x16" || uploads != 0 {
		t.Fatalf("only a lone Ctrl+V is a paste: typed %q, %d uploads", typed, uploads)
	}
}

func TestRemoteImagePaste_CtrlQStillDetaches(t *testing.T) {
	rec := &pasteRecorder{}
	input := sshAttachInput{writer: rec, paste: newTestPaste(rec, pngBytes, nil, "claude", false)}
	if detached, _ := input.forward([]byte{0x11}); !detached {
		t.Fatal("Ctrl+Q detaches with paste on")
	}
}

func TestRemoteImagePaste_TooLargeSaysSoAndSendsNothing(t *testing.T) {
	rec := &pasteRecorder{}
	big := make([]byte, MaxImageUploadBytes+1)
	input := sshAttachInput{writer: rec, paste: newTestPaste(rec, big, nil, "claude", false)}
	if _, err := input.forward([]byte{0x16}); err != nil {
		t.Fatal(err)
	}
	typed, notices, uploads := rec.snapshot()
	if typed != "" || uploads != 0 || len(notices) != 1 || notices[0] != pasteTooLargeNotice() {
		t.Fatalf("over 20 MiB: one line, nothing typed or sent: typed %q, %d uploads, %q", typed, uploads, notices)
	}
}

func TestRemoteImagePaste_OldRemoteSaysSoInOneLine(t *testing.T) {
	rec := &pasteRecorder{}
	input := sshAttachInput{writer: rec, paste: newTestPaste(rec, pngBytes, ErrImageUploadUnsupported, "claude", false)}
	if _, err := input.forward([]byte{0x16}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, n, _ := rec.snapshot(); return len(n) == 2 })
	typed, notices, _ := rec.snapshot()
	if typed != "" || len(notices) != 2 || notices[1] != pasteTooOldNotice("deckbox") || strings.Contains(notices[1], "\n") {
		t.Fatalf("an old remote: one line, nothing typed: typed %q, notices %q", typed, notices)
	}
	rec2 := &pasteRecorder{}
	input2 := sshAttachInput{writer: rec2, paste: newTestPaste(rec2, pngBytes, errors.New("ssh: connect timed out\nmore"), "claude", false)}
	_, _ = input2.forward([]byte{0x16})
	waitFor(t, func() bool { _, n, _ := rec2.snapshot(); return len(n) == 2 })
	if _, n, _ := rec2.snapshot(); len(n) != 2 || !strings.HasPrefix(n[1], "agent-deck: the image could not reach deckbox: ssh: connect timed out") ||
		strings.Contains(n[1], "\n") {
		t.Fatalf("a failed upload: one line with the reason: %q", n)
	}
}

func TestRemoteImagePaste_PathFormFollowsTheHarness(t *testing.T) {
	cases := map[string]string{"claude": "@/a.png ", "gemini": "@/a.png ", "codex": "/a.png ", "": "/a.png "}
	for tool, want := range cases {
		if got := pastedPaths([]string{"/a.png"}, tool); got != want {
			t.Errorf("%q: %q, want %q", tool, got, want)
		}
	}
}

func TestSSHRunnerUploadImageSendsTheBytesToImageUpload(t *testing.T) {
	r := NewSSHRunner("deckbox", RemoteConfig{Host: "u@box"})
	var gotArgs []string
	var gotStdin []byte
	r.runStdinFn = func(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
		gotArgs, gotStdin = append([]string(nil), args...), stdin
		return []byte(`{"path":"/srv/p/macapp-uploads/s1/x.png","bytes":25}` + "\n"), nil
	}
	path, err := r.UploadImage(context.Background(), "s1", pngBytes, "png")
	if err != nil || path != "/srv/p/macapp-uploads/s1/x.png" {
		t.Fatalf("path %q err %v", path, err)
	}
	i := slices.Index(gotArgs, "image-upload")
	if i < 1 || gotArgs[i-1] != "session" || gotArgs[i+1] != "s1" || gotArgs[i+2] != "--name" ||
		!strings.HasSuffix(gotArgs[i+3], ".png") || gotArgs[i+4] != "--json" || !bytes.Equal(gotStdin, pngBytes) {
		t.Fatalf("args %v, stdin %d bytes", gotArgs, len(gotStdin))
	}

	r.runStdinFn = func(context.Context, []byte, ...string) ([]byte, error) {
		return []byte("Unknown session command: image-upload"), errors.New("exit status 1")
	}
	if _, err := r.UploadImage(context.Background(), "s1", pngBytes, "png"); !errors.Is(err, ErrImageUploadUnsupported) {
		t.Fatalf("an old remote is told apart: %v", err)
	}
	r.runStdinFn = func(context.Context, []byte, ...string) ([]byte, error) { return []byte(`{"path":"relative.png"}`), nil }
	if _, err := r.UploadImage(context.Background(), "s1", pngBytes, "png"); err == nil {
		t.Fatal("a path that is not absolute is refused")
	}
}

func TestParseOSAScriptData(t *testing.T) {
	if got := parseOSAScriptData([]byte("«data PNGf89504E470D0A1A0A»\n"), "PNGf"); !bytes.Equal(got, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("got % x", got)
	}
	for _, bad := range []string{"", "missing value", "«data TIFF4D4D»", "«data PNGfZZ»"} {
		if got := parseOSAScriptData([]byte(bad), "PNGf"); got != nil {
			t.Fatalf("%q: % x", bad, got)
		}
	}
}

func TestBracketedPasteTrackerFollowsTheRemoteAcrossReads(t *testing.T) {
	var tr bracketedPasteTracker
	tr.observe([]byte("draw\x1b[?20"))
	tr.observe([]byte("04h more"))
	if !tr.enabled() {
		t.Fatal("a split CSI ? 2004 h turns it on")
	}
	tr.observe([]byte("x\x1b[?2004l"))
	if tr.enabled() {
		t.Fatal("CSI ? 2004 l turns it off")
	}
}

func TestAttachOutputNoticeDrawsOnTheLastRowAndRestoresTheCursor(t *testing.T) {
	var buf bytes.Buffer
	o := &attachOutput{w: &buf}
	o.notice("agent-deck: hi", 40)
	if got := buf.String(); got != "\x1b7\x1b[40;1H\x1b[2K\x1b[7m agent-deck: hi \x1b[0m\x1b8" {
		t.Fatalf("%q", got)
	}
	buf.Reset()
	o.notice("", 40)
	if got := buf.String(); got != "\x1b7\x1b[40;1H\x1b[2K\x1b8" {
		t.Fatalf("%q", got)
	}
}
