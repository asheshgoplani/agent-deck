package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// Owner report 2026-10-18: in a terminal attached to a remote session, Ctrl+V
// with a screenshot on the Mac did nothing (Claude on the host reads the
// host's clipboard). The attach now sends the image up and types its host
// path. These tests drive sshAttachInput, the attach's own input path.

var pngBytes = []byte("\x89PNG\r\n\x1a\n" + "rest-of-a-picture")

// Panes as `session output --pane` returns them.
const (
	claudeComposerPane = "⏺ Done.\n\n" +
		"\x1b[38;5;244m────────────────────────────────────────── work ─\n" +
		"\x1b[38;5;246m❯\u00a0\x1b[39m\n" +
		"\x1b[38;5;244m──────────────────────────────────────────────────\n" +
		"  Model X | Context 41.0% | 5h 24.0% | 7d 57.0%\n" +
		"  ⏵⏵ bypass permissions on · 1 shell\n\n"
	vimPane          = "package main\n\nfunc main() {\n}\n~\n~\n~\n\"main.go\" 4L, 29B                 1,1           All\n"
	shellPane        = "user@box:~/work$ ls\nREADME.md  main.go\nuser@box:~/work$ \n"
	claudeDialogPane = "⏺ Bash(rm -rf build)\n\n" +
		"╭──────────────────────────────╮\n│ Do you want to proceed?      │\n│ ❯ 1. Yes                     │\n│   2. No                      │\n╰──────────────────────────────╯\n"
)

type pasteRecorder struct {
	mu      sync.Mutex
	typed   bytes.Buffer
	notices []string
	alerts  []string
	uploads [][]byte
	exts    []string
	targets int
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

func (r *pasteRecorder) alertsSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.alerts...)
}

func newTestPaste(rec *pasteRecorder, image []byte, uploadErr error, tool string, bracketed bool) *remoteImagePaste {
	return newTestPasteOn(rec, image, uploadErr, tool, claudeComposerPane, nil, bracketed)
}

func newTestPasteOn(rec *pasteRecorder, image []byte, uploadErr error, tool, pane string, targetErr error, bracketed bool) *remoteImagePaste {
	return &remoteImagePaste{
		remote:    "deckbox",
		readImage: func() clipboardRead { return clipboardRead{data: image, ext: "png"} },
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
		target: func(context.Context) (string, string, error) {
			rec.mu.Lock()
			rec.targets++
			rec.mu.Unlock()
			return tool, pane, targetErr
		},
		write:  func(p []byte) error { _, err := rec.Write(p); return err },
		notice: func(text string) { rec.mu.Lock(); rec.notices = append(rec.notices, text); rec.mu.Unlock() },
		alert: func(text string) {
			rec.mu.Lock()
			rec.notices = append(rec.notices, text)
			rec.alerts = append(rec.alerts, text)
			rec.mu.Unlock()
		},
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

// Lander review of #2775: Ctrl+V was taken in every program whenever the clipboard held an image. It is now
// taken only when the session's agent reads a pasted @path and its composer is on screen.
func TestRemoteImagePaste_EditorsShellsAndDialogsKeepCtrlV(t *testing.T) {
	cases := []struct {
		name, tool, pane string
		err              error
	}{
		{"vim in a Claude session's pane", "claude", vimPane, nil},
		{"a shell session", "shell", shellPane, nil},
		{"a shell session that shows a Claude-looking pane", "shell", claudeComposerPane, nil},
		{"Codex (no @path)", "codex", claudeComposerPane, nil},
		{"Claude's permission dialog", "claude", claudeDialogPane, nil},
		{"the pane cannot be read", "claude", "", errors.New("ssh: timeout")},
		{"an empty pane", "claude", "", nil},
	}
	for _, tc := range cases {
		rec := &pasteRecorder{}
		input := sshAttachInput{writer: rec, paste: newTestPasteOn(rec, pngBytes, nil, tc.tool, tc.pane, tc.err, false)}
		if _, err := input.forward([]byte{0x16}); err != nil {
			t.Fatal(err)
		}
		typed, notices, uploads := rec.snapshot()
		if typed != "\x16" || uploads != 0 || len(notices) != 0 {
			t.Fatalf("%s: Ctrl+V must reach the program as typed: typed %q, %d uploads, notices %q", tc.name, typed, uploads, notices)
		}
	}
}

func TestRemoteImagePaste_NoImageAsksTheRemoteNothing(t *testing.T) {
	rec := &pasteRecorder{}
	input := sshAttachInput{writer: rec, paste: newTestPaste(rec, nil, nil, "claude", false)}
	_, _ = input.forward([]byte{0x16})
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.targets != 0 {
		t.Fatalf("no image on the clipboard: no remote call (%d)", rec.targets)
	}
}

func TestRemotePasteImagesSetting(t *testing.T) {
	var cfg UserConfig
	if _, err := toml.Decode("[remote]\npaste_images = false\n", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Remote.PasteImagesEnabled() {
		t.Fatal("[remote] paste_images = false turns image paste off")
	}
	var unset UserConfig
	if !unset.Remote.PasteImagesEnabled() {
		t.Fatal("image paste is on by default")
	}
	if _, err := toml.Decode("[remote]\npaste_images = true\n", &cfg); err != nil || !cfg.Remote.PasteImagesEnabled() {
		t.Fatalf("paste_images = true: %v", err)
	}
}

func TestRemoteImagePaste_OffMeansNoRoute(t *testing.T) {
	off := false
	restore := resetUserConfigCache(t, &UserConfig{Remote: RemoteAttachSettings{PasteImages: &off}})
	if p := NewSSHRunner("deckbox", RemoteConfig{Host: "u@box"}).imagePaste("s1"); p != nil {
		t.Fatal("with paste_images = false the attach takes no key")
	}
	restore()
	restore = resetUserConfigCache(t, &UserConfig{})
	defer restore()
	if p := NewSSHRunner("deckbox", RemoteConfig{Host: "u@box"}).imagePaste("s1"); p == nil {
		t.Fatal("by default the attach has the paste route")
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
	r.runStdinFn = func(context.Context, []byte, ...string) ([]byte, error) {
		return []byte(`{"path":"relative.png"}`), nil
	}
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

// Lander follow-up (7): the notice drew on the last row between DECSC/DECRC and clobbered the remote program's saved
// cursor. It is the terminal's title now: no cursor or screen sequence at all.
func TestAttachOutputNoticeIsTheTitleAndNeverMovesTheCursor(t *testing.T) {
	var buf bytes.Buffer
	o := &attachOutput{w: &buf}
	o.progress(pasteUploadingNotice("deckbox"))
	if got := buf.String(); got != titlePush+"\x1b]2;"+pasteUploadingNotice("deckbox")+"\x07" {
		t.Fatalf("%q", got)
	}
	o.alert(pasteFailedNotice("deckbox", errors.New("boom")))
	if got := buf.String(); !strings.HasSuffix(got, "\x07\a") {
		t.Fatalf("a failure also rings the bell: %q", got)
	}
	o.notice("")
	if got := buf.String(); !strings.HasSuffix(got, titlePop) {
		t.Fatalf("%q", got)
	}
	for _, seq := range []string{"\x1b7", "\x1b8", "\x1b[s", "\x1b[u", "H"} {
		if strings.Contains(buf.String(), seq) && seq != "H" {
			t.Fatalf("no cursor save, restore or move: %q", buf.String())
		}
	}
	if strings.ContainsAny(pasteFailedNotice("deckbox", errors.New("bad\x1b]2;x\x07\nmore")), "\x1b\x07\n") {
		t.Fatal("an error text cannot carry control characters into the title")
	}
}

// Lander follow-up (B2): a Ctrl+V while an upload runs was dropped.
func TestRemoteImagePaste_CtrlVDuringAnUploadIsForwarded(t *testing.T) {
	rec := &pasteRecorder{}
	release := make(chan struct{})
	p := newTestPaste(rec, pngBytes, nil, "claude", false)
	upload := p.upload
	p.upload = func(ctx context.Context, data []byte, ext string) (string, error) {
		<-release
		return upload(ctx, data, ext)
	}
	input := sshAttachInput{writer: rec, paste: p}
	_, _ = input.forward([]byte{0x16})
	_, _ = input.forward([]byte{0x16}) // while the first is uploading
	typed, _, _ := rec.snapshot()
	if typed != "\x16" {
		t.Fatalf("the second Ctrl+V reaches the program as typed: %q", typed)
	}
	close(release)
	waitFor(t, func() bool { typed, _, _ := rec.snapshot(); return strings.Contains(typed, "@/") })
}

// Lander follow-up (4): an image over the cap was taken for "no image" on macOS and forwarded silently.
func TestRemoteImagePaste_ClipboardOverTheCapSaysSoOnlyOverTheComposer(t *testing.T) {
	for _, tc := range []struct {
		pane       string
		wantNotice bool
	}{{claudeComposerPane, true}, {vimPane, false}} {
		rec := &pasteRecorder{}
		p := newTestPasteOn(rec, nil, nil, "claude", tc.pane, nil, false)
		p.readImage = func() clipboardRead { return clipboardRead{tooLarge: true} }
		input := sshAttachInput{writer: rec, paste: p}
		_, _ = input.forward([]byte{0x16})
		typed, notices, uploads := rec.snapshot()
		if tc.wantNotice && (typed != "" || uploads != 0 || len(notices) != 1 || notices[0] != pasteTooLargeNotice()) {
			t.Fatalf("over an agent's composer: the too-large line, nothing sent: %q %d %q", typed, uploads, notices)
		}
		if !tc.wantNotice && (typed != "\x16" || len(notices) != 0) {
			t.Fatalf("in vim the key goes on as typed: %q %q", typed, notices)
		}
	}
}

func TestBoundedOutputReportsAnAnswerOverTheCap(t *testing.T) {
	data, status := boundedOutput(5*time.Second, 1000, "sh", "-c", "head -c 100 /dev/zero")
	if status != clipRead || len(data) != 100 {
		t.Fatalf("under the cap: %d bytes, status %v", len(data), status)
	}
	start := time.Now()
	data, status = boundedOutput(5*time.Second, 10, "sh", "-c", "yes")
	over := status == clipOverCap
	if !over || data != nil || time.Since(start) > 3*time.Second {
		t.Fatalf("over the cap: over=%v, %d bytes, %v (the reader is stopped at once)", over, len(data), time.Since(start))
	}
}

// Lander follow-up (5): the upload had the 30 s command timeout.
func TestSSHRunnerUploadImageHasItsOwnDeadline(t *testing.T) {
	r := NewSSHRunner("deckbox", RemoteConfig{Host: "u@box", CommandTimeoutSeconds: 1})
	var left time.Duration
	r.runStdinFn = func(ctx context.Context, _ []byte, _ ...string) ([]byte, error) {
		if deadline, ok := ctx.Deadline(); ok {
			left = time.Until(deadline)
		}
		return []byte(`{"path":"/p/x.png"}`), nil
	}
	if _, err := r.UploadImage(context.Background(), "s1", pngBytes, "png"); err != nil {
		t.Fatal(err)
	}
	if left < 100*time.Second {
		t.Fatalf("the upload deadline is its own 120 s, not the 1 s command timeout: %v", left)
	}
}

// Lander follow-up (6): only \n and NUL were refused in the host path that gets typed.
func TestSSHRunnerUploadImageRefusesAPathThatCannotBeTypedSafely(t *testing.T) {
	for _, bad := range []string{"/p/a\x1b[2Jb.png", "/p/a\tb.png", "/p/a b.png", "/p/a\rb.png", "/p/a\u0085b.png", "/p/a\x7fb.png"} {
		r := NewSSHRunner("deckbox", RemoteConfig{Host: "u@box"})
		answer, _ := json.Marshal(map[string]string{"path": bad})
		r.runStdinFn = func(context.Context, []byte, ...string) ([]byte, error) { return answer, nil }
		if path, err := r.UploadImage(context.Background(), "s1", pngBytes, "png"); err == nil {
			t.Fatalf("%q was accepted as %q", bad, path)
		}
	}
}

// Lander follow-up (3): a keystroke could stall up to ~7 s (two osascript reads of 2 s and a 3 s pane call). One
// clipboard read per Ctrl+V, and the pane check gets at most 2 s.
func TestRemoteImagePaste_TheProbeIsOneReadAndABoundedCheck(t *testing.T) {
	rec := &pasteRecorder{}
	p := newTestPaste(rec, pngBytes, nil, "claude", false)
	reads := 0
	p.readImage = func() clipboardRead { reads++; return clipboardRead{data: pngBytes, ext: "png"} }
	var left time.Duration
	p.target = func(ctx context.Context) (string, string, error) {
		if deadline, ok := ctx.Deadline(); ok {
			left = time.Until(deadline)
		}
		return "claude", claudeComposerPane, nil
	}
	input := sshAttachInput{writer: rec, paste: p}
	_, _ = input.forward([]byte{0x16})
	if reads != 1 || left <= 0 || left > 2*time.Second {
		t.Fatalf("one clipboard read (%d) and a pane check bounded to 2 s (%v)", reads, left)
	}
	if clipboardReadTimeout+pasteTargetTimeout > 4*time.Second {
		t.Fatalf("worst-case hold %v", clipboardReadTimeout+pasteTargetTimeout)
	}
}

// Port review: the path check also refuses invisible format characters (a bidi
// override or a zero-width character shows the owner a different path than
// the one typed) and invalid UTF-8, and still takes an ordinary host path.
func TestTypeablePathRefusesInvisibleAndInvalidText(t *testing.T) {
	for _, bad := range []string{"", "/p/a‮b.png", "/p/a​b.png", "/p/a\ufeffb.png", "/p/a\x9bb.png", "/p/a\xc2\x9b.png", "/p/a b.png", "/p/a\x00b.png"} {
		if typeablePath(bad) {
			t.Fatalf("%q was taken as typeable", bad)
		}
	}
	for _, good := range []string{"/home/u/.agent-deck/macapp-uploads/s1/3f2a.png", "/Users/ü/日本/x-y_z.(1).png", "/p/it's;$(x).png"} {
		if !typeablePath(good) {
			t.Fatalf("%q was refused", good)
		}
	}
}

// Port review: every notice goes into an OSC 2 title, including the local
// TUI's own notices through tmux.SetAttachNoticeSink. A BEL or ESC in any of
// them would end the title early and the rest would act in the owner's
// terminal, so notice cleans the text itself.
func TestAttachOutputNoticeCannotEndTheTitleEarly(t *testing.T) {
	var buf bytes.Buffer
	o := &attachOutput{w: &buf}
	o.notice("agent-deck v1.2.3\x07\x1b[2J\x1b]0;owned\x9c\xff is installed: detach to restart")
	got := strings.TrimPrefix(buf.String(), titlePush)
	body := strings.TrimSuffix(strings.TrimPrefix(got, "\x1b]2;"), "\x07")
	if !strings.HasPrefix(got, "\x1b]2;") || strings.ContainsAny(body, "\x07\x1b\u009c") || !utf8.ValidString(body) {
		t.Fatalf("the title carries a terminator or an escape: %q", got)
	}
}
