package clipboard

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// fakeClipboard swaps the native tool, the OSC 52 writer and the environment
// for one test and records which route a copy took.
type fakeClipboard struct {
	env       map[string]string
	nativeErr error
	oscErr    error
	native    []string
	osc       []string
}

func installFake(t *testing.T, f *fakeClipboard) {
	t.Helper()
	oldNative, oldOSC, oldEnv := nativeCopyFn, osc52WriteFn, getenv
	nativeCopyFn = func(_ context.Context, text string) (string, error) {
		f.native = append(f.native, text)
		if f.nativeErr != nil {
			return "", f.nativeErr
		}
		return "xclip", nil
	}
	osc52WriteFn = func(text string) error {
		f.osc = append(f.osc, text)
		return f.oscErr
	}
	getenv = func(k string) string { return f.env[k] }
	t.Cleanup(func() { nativeCopyFn, osc52WriteFn, getenv = oldNative, oldOSC, oldEnv })
}

// Inside tmux the copy must reach the user's terminal whichever of
// set-clipboard (plain OSC 52) or allow-passthrough (DCS form) is on: a TUI
// running in a pane without allow-passthrough lost every copy before.
func TestGenerateOSC52_InTmuxSendsPlainAndPassthrough(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("hello"))
	plain := "\x1b]52;c;" + encoded + "\x07"
	dcs := "\x1bPtmux;\x1b" + plain + "\x1b\\"

	seq := generateOSC52(encoded, true)
	if !strings.HasPrefix(seq, plain) {
		t.Fatalf("in tmux the plain OSC 52 (for set-clipboard on) must come first; got %q", seq)
	}
	if !strings.Contains(seq, dcs) {
		t.Fatalf("in tmux the DCS passthrough form (for allow-passthrough on) is missing; got %q", seq)
	}
	if seq != plain+dcs {
		t.Fatalf("generateOSC52(inTmux) = %q, want %q", seq, plain+dcs)
	}
}

// Over SSH a native tool writes the host's clipboard, not the user's: OSC 52
// must win even when xclip "succeeds".
func TestCopy_OverSSHPrefersOSC52(t *testing.T) {
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		t.Run(key, func(t *testing.T) {
			f := &fakeClipboard{env: map[string]string{key: "x"}}
			installFake(t, f)
			res, err := Copy("from deckbox", true)
			if err != nil {
				t.Fatalf("Copy: %v", err)
			}
			if res.Method != "osc52" || len(f.osc) != 1 || len(f.native) != 0 {
				t.Fatalf("over SSH (%s) want OSC 52 only; method=%q osc=%d native=%d", key, res.Method, len(f.osc), len(f.native))
			}
		})
	}
}

func TestCopy_LocalKeepsNativeFirst(t *testing.T) {
	f := &fakeClipboard{env: map[string]string{}}
	installFake(t, f)
	res, err := Copy("local desktop", true)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if res.Method != "xclip" || len(f.native) != 1 || len(f.osc) != 0 {
		t.Fatalf("local copy want native only; method=%q osc=%d native=%d", res.Method, len(f.osc), len(f.native))
	}
}

func TestCopy_OverSSHFallsBackToNativeWhenOSC52Fails(t *testing.T) {
	f := &fakeClipboard{env: map[string]string{"SSH_TTY": "/dev/pts/1"}, oscErr: errors.New("no tty")}
	installFake(t, f)
	res, err := Copy("no tty", true)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if res.Method != "xclip" {
		t.Fatalf("OSC 52 failed over SSH; want the native fallback, got %q", res.Method)
	}
}

func TestCopy_OverSSHWithoutOSC52SupportUsesNative(t *testing.T) {
	f := &fakeClipboard{env: map[string]string{"SSH_CONNECTION": "x"}}
	installFake(t, f)
	res, err := Copy("apple terminal", false)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if res.Method != "xclip" || len(f.osc) != 0 {
		t.Fatalf("terminal without OSC 52: want native only; method=%q osc=%d", res.Method, len(f.osc))
	}
}
