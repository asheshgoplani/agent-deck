package session

import (
	"slices"
	"sync"
	"testing"
	"time"
)

// ASK-92: the TUI runs on a box reached over plain ssh, the owner presses
// Ctrl+V over Claude's composer with a screenshot on their own machine, and
// Claude answers "no image found" (it reads the box's clipboard). The attach
// now says once where images can be pasted from.

type hintRecorder struct {
	mu      sync.Mutex
	notices []string
}

func (r *hintRecorder) notice(text string) {
	r.mu.Lock()
	r.notices = append(r.notices, text)
	r.mu.Unlock()
}

func (r *hintRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.notices)
}

func overSSH(key string) string {
	if key == "SSH_CONNECTION" {
		return "10.0.0.2 52144 10.0.0.9 22"
	}
	return ""
}

func newTestHint(rec *hintRecorder, getenv func(string) string, image []byte, tool, pane string) *SSHPasteHint {
	return newSSHPasteHint(getenv, func() clipboardRead { return clipboardRead{data: image, ext: "png"} }, tool,
		func() string { return pane }, rec.notice)
}

func TestSSHPasteHint_CtrlVOverSSHWithNoImageShowsTheHintOnce(t *testing.T) {
	rec := &hintRecorder{}
	hint := newTestHint(rec, overSSH, nil, "claude", claudeComposerPane)
	if hint == nil {
		t.Fatal("SSH_CONNECTION set and a Claude session: the hint is armed")
	}
	hint.CtrlV()
	waitFor(t, func() bool { return len(rec.snapshot()) == 1 })
	for i := 0; i < 3; i++ {
		hint.CtrlV()
	}
	time.Sleep(50 * time.Millisecond)
	notices := rec.snapshot()
	if len(notices) != 1 || notices[0] != sshPasteHintNotice() {
		t.Fatalf("one hint per attach, however often Ctrl+V is pressed: %q", notices)
	}
	if n := len(notices[0]); n > 80 {
		t.Fatalf("the hint is %d characters; tmux cuts a status line at the terminal width, so it fits 80", n)
	}
}

func TestSSHPasteHint_NoHintWhenItWouldBeWrong(t *testing.T) {
	cases := []struct {
		name   string
		getenv func(string) string
		image  []byte
		tool   string
		pane   string
	}{
		{"an image on this box's clipboard (Claude pastes it)", overSSH, pngBytes, "claude", claudeComposerPane},
		{"vim in a Claude session's pane", overSSH, nil, "claude", vimPane},
		{"Claude's permission dialog", overSSH, nil, "claude", claudeDialogPane},
		{"the pane cannot be read", overSSH, nil, "claude", ""},
	}
	for _, tc := range cases {
		rec := &hintRecorder{}
		hint := newTestHint(rec, tc.getenv, tc.image, tc.tool, tc.pane)
		if hint == nil {
			t.Fatalf("%s: the hint is armed over ssh for Claude", tc.name)
		}
		hint.check()
		if n := rec.snapshot(); len(n) != 0 {
			t.Fatalf("%s: no hint: %q", tc.name, n)
		}
	}
	if newTestHint(&hintRecorder{}, func(string) string { return "" }, nil, "claude", claudeComposerPane) != nil {
		t.Fatal("not over ssh (SSH_CONNECTION unset): no hint at all")
	}
	for _, tool := range []string{"shell", "codex"} {
		if newTestHint(&hintRecorder{}, overSSH, nil, tool, claudeComposerPane) != nil {
			t.Fatalf("%s reads no pasted image: no hint", tool)
		}
	}
	var none *SSHPasteHint
	none.CtrlV() // a nil hint is a no-op
}

func TestSSHPasteHint_CtrlVNeverWaitsForTheCheck(t *testing.T) {
	rec := &hintRecorder{}
	release := make(chan struct{})
	hint := newSSHPasteHint(overSSH, func() clipboardRead { <-release; return clipboardRead{} }, "claude",
		func() string { return claudeComposerPane }, rec.notice)
	done := make(chan struct{})
	go func() { hint.CtrlV(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Ctrl+V blocked on the clipboard read")
	}
	close(release)
	waitFor(t, func() bool { return len(rec.snapshot()) == 1 })
	if n := rec.snapshot(); len(n) != 1 {
		t.Fatalf("the hint still arrives once the check finishes: %q", n)
	}
}

// #2775 stays as it was: a remote attach from the owner's machine (where
// SSH_CONNECTION can be set too, e.g. a Mac reached over ssh) uploads the
// clipboard image and types its host path, with no hint among its notices.
// The hint stays off this path because only the TUI's own attach sets
// AttachOptions.OnCtrlV (internal/ui withSSHPasteHint); this test pins the
// upload itself under SSH_CONNECTION.
func TestRemoteImagePaste_RemoteAttachUnderSSHStillPastesWithNoHint(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.0.0.2 52144 10.0.0.9 22")
	rec := &pasteRecorder{}
	input := sshAttachInput{writer: rec, paste: newTestPaste(rec, pngBytes, nil, "claude", false)}
	if _, err := input.forward([]byte{0x16}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { typed, _, _ := rec.snapshot(); return typed != "" })
	typed, notices, uploads := rec.snapshot()
	if typed != "@/home/u/.agent-deck/profiles/default/macapp-uploads/s1/A.png " || uploads != 1 {
		t.Fatalf("the image goes up and its path is typed: typed %q, %d uploads", typed, uploads)
	}
	if !slices.Equal(notices, []string{pasteUploadingNotice("deckbox"), ""}) || slices.Contains(notices, sshPasteHintNotice()) {
		t.Fatalf("only the upload's own notices, never the ssh hint: %q", notices)
	}
}
