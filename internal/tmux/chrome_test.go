//go:build !windows

package tmux

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

// TestEmitITermBadge_EncodesTitleBase64 asserts that the helper produces
// exactly the OSC 1337 SetBadgeFormat sequence iTerm2 expects: the literal
// prefix, the base64-encoded title bytes, and a BEL terminator.
func TestEmitITermBadge_EncodesTitleBase64(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("AGENTDECK_ITERM_BADGE", "")

	var buf bytes.Buffer
	emitITermBadge(&buf, "myapp", true)

	want := "\x1b]1337;SetBadgeFormat=" + base64.StdEncoding.EncodeToString([]byte("myapp")) + "\a"
	require.Equal(t, want, buf.String(),
		"emitITermBadge must write OSC 1337;SetBadgeFormat=<base64(title)>BEL")
}

// TestEmitITermBadge_EmptyTitleClears asserts that an empty title produces
// the badge-clear form (empty base64 payload), which iTerm2 interprets as
// "remove the badge" — used on detach to leave the terminal in a clean state.
func TestEmitITermBadge_EmptyTitleClears(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("AGENTDECK_ITERM_BADGE", "")

	var buf bytes.Buffer
	emitITermBadge(&buf, "", true)

	require.Equal(t, "\x1b]1337;SetBadgeFormat=\a", buf.String(),
		"empty title must emit the no-payload form to clear the badge")
}

// TestEmitITermBadge_NoOpOutsideITerm2 ensures we don't write iTerm2-specific
// escape sequences to terminals that won't parse them — they would otherwise
// appear as literal garbage in the user's terminal output.
func TestEmitITermBadge_NoOpOutsideITerm2(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	t.Setenv("ITERM_SESSION_ID", "")
	t.Setenv("LC_TERMINAL", "")
	t.Setenv("AGENTDECK_ITERM_BADGE", "")

	var buf bytes.Buffer
	emitITermBadge(&buf, "anything", true)

	require.Empty(t, buf.Bytes(),
		"emitITermBadge must not write anything when not running inside iTerm2; got %q", buf.String())
}

// TestEmitITermBadge_HonorsLCTerminal verifies the SSH-friendly fallback:
// when only LC_TERMINAL=iTerm2 is set (TERM_PROGRAM does not propagate over
// ssh), we still detect iTerm2 and emit the badge. Mirrors the gate the
// external bash hook in tarek-eq-scripts uses.
func TestEmitITermBadge_HonorsLCTerminal(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("ITERM_SESSION_ID", "")
	t.Setenv("LC_TERMINAL", "iTerm2")
	t.Setenv("AGENTDECK_ITERM_BADGE", "")

	var buf bytes.Buffer
	emitITermBadge(&buf, "remote", true)

	require.NotEmpty(t, buf.Bytes(),
		"emitITermBadge must honor LC_TERMINAL=iTerm2 (SSH propagation path)")
}

// TestEmitITermBadge_ConfigDisabledSuppressesEmit covers the
// [terminal].iterm_badge=false config default. With the env var unset,
// configEnabled=false must suppress the OSC write — that's the whole
// point of the opt-in default for users who run their own badge scheme.
func TestEmitITermBadge_ConfigDisabledSuppressesEmit(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("AGENTDECK_ITERM_BADGE", "")

	var buf bytes.Buffer
	emitITermBadge(&buf, "myapp", false)

	require.Empty(t, buf.Bytes(),
		"configEnabled=false must suppress chrome emits even on iTerm2; got %q", buf.String())
}

// TestEmitITermBadge_EnvForceEnableOverridesConfigOff pins the "ad-hoc
// enable trumps persistent off" direction: config defaults the badge to
// off, but AGENTDECK_ITERM_BADGE=1 forces it on for this run. Each truthy
// value variant is asserted because the helper accepts a small set.
func TestEmitITermBadge_EnvForceEnableOverridesConfigOff(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "iTerm.app")

	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("AGENTDECK_ITERM_BADGE", v)

			var buf bytes.Buffer
			emitITermBadge(&buf, "myapp", false) // config off, env says on

			require.NotEmpty(t, buf.Bytes(),
				"AGENTDECK_ITERM_BADGE=%q must force-enable when config is off", v)
		})
	}
}

// TestEmitITermBadge_EnvForceDisableOverridesConfigOn pins the "ad-hoc
// disable trumps persistent on" direction: config has badge on, but
// AGENTDECK_ITERM_BADGE=0 forces it off for this run. Each falsy value
// variant is asserted because the helper accepts a small set.
func TestEmitITermBadge_EnvForceDisableOverridesConfigOn(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "iTerm.app")

	for _, v := range []string{"0", "false", "FALSE", "no", "off"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("AGENTDECK_ITERM_BADGE", v)

			var buf bytes.Buffer
			emitITermBadge(&buf, "myapp", true) // config on, env says off

			require.Empty(t, buf.Bytes(),
				"AGENTDECK_ITERM_BADGE=%q must force-disable when config is on; got %q",
				v, buf.String())
		})
	}
}

// TestEmitITermBadge_EnvGarbageDefersToConfig pins fail-open: a typo or
// otherwise unrecognised env value must not silently flip the user's
// persistent setting. Garbage falls through to configEnabled.
func TestEmitITermBadge_EnvGarbageDefersToConfig(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("AGENTDECK_ITERM_BADGE", "maybe")

	var bufOff bytes.Buffer
	emitITermBadge(&bufOff, "myapp", false)
	require.Empty(t, bufOff.Bytes(),
		"garbage env value with config off must remain off (not silently force-on); got %q", bufOff.String())

	var bufOn bytes.Buffer
	emitITermBadge(&bufOn, "myapp", true)
	require.NotEmpty(t, bufOn.Bytes(),
		"garbage env value with config on must remain on (not silently force-off)")
}

// TestFormatITermBadgeOSC_RoundTrip pins the on-the-wire byte format. Both
// emit paths (direct stdout in Attach, /dev/tty in the rename hook) share
// this string, so a regression here breaks both.
func TestFormatITermBadgeOSC_RoundTrip(t *testing.T) {
	require.Equal(t,
		"\x1b]1337;SetBadgeFormat="+base64.StdEncoding.EncodeToString([]byte("myapp"))+"\a",
		formatITermBadgeOSC("myapp"),
		"formatITermBadgeOSC must produce ESC ]1337;SetBadgeFormat=<b64>BEL")

	require.Equal(t, "\x1b]1337;SetBadgeFormat=\a", formatITermBadgeOSC(""),
		"empty title must produce the badge-clear form (no payload)")
}

// TestFormatITermBadgeOSCViaTmux_DCSEnvelope pins the tmux DCS passthrough
// wrapping rule: the inner OSC's lone ESC is doubled (\x1b\x1b) so tmux
// strips one and forwards the other. ESC P tmux ; ... ESC \ delimits.
// Without this exact shape the rename-hook badge update silently disappears
// inside tmux instead of reaching iTerm2.
func TestFormatITermBadgeOSCViaTmux_DCSEnvelope(t *testing.T) {
	got := formatITermBadgeOSCViaTmux("myapp")

	want := "\x1bPtmux;\x1b\x1b]1337;SetBadgeFormat=" +
		base64.StdEncoding.EncodeToString([]byte("myapp")) +
		"\x07\x1b\\"
	require.Equal(t, want, got,
		"DCS-wrapped form must be ESC P tmux ; ESC ESC <OSC inner> ESC \\")

	require.True(t, strings.HasPrefix(got, "\x1bPtmux;"),
		"must open with the DCS tmux prefix")
	require.True(t, strings.HasSuffix(got, "\x1b\\"),
		"must close with the ST terminator (ESC \\)")
}

// TestEmitITermBadgeViaTty_NoOpOutsideITerm2 ensures the rename-hook helper
// is safe to call from any terminal — outside iTerm2 we must not even
// attempt to open /dev/tty (which would still succeed and write garbage
// into the user's pane that the wrong terminal can't parse).
func TestEmitITermBadgeViaTty_NoOpOutsideITerm2(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	t.Setenv("ITERM_SESSION_ID", "")
	t.Setenv("LC_TERMINAL", "")
	t.Setenv("AGENTDECK_ITERM_BADGE", "")

	// Cannot meaningfully assert on /dev/tty contents from a unit test, but
	// we can confirm the function returns without panicking and consults
	// the gate first. iTerm2Active is the early-return barrier; if it's
	// honored, no /dev/tty open attempt happens.
	require.NotPanics(t, func() {
		EmitITermBadgeViaTty("anything", true)
	}, "EmitITermBadgeViaTty must be a silent no-op outside iTerm2")
}

// TestSession_SetTerminalChromeEnabled pins the setter contract: a fresh
// Session built via NewSession defaults to disabled (opt-in), and
// SetTerminalChromeEnabled flips the bit returned by the read accessor.
// Mirrors TestSession_SetInjectStatusLine but with the inverse default —
// the iTerm2 badge is opt-in because most users already drive their badge
// from their shell prompt.
func TestSession_SetTerminalChromeEnabled(t *testing.T) {
	sess := NewSession("test", "/tmp")
	require.False(t, sess.terminalChromeIsEnabled(),
		"NewSession must default terminalChromeEnabled=false (opt-in)")

	sess.SetTerminalChromeEnabled(true)
	require.True(t, sess.terminalChromeIsEnabled(),
		"SetTerminalChromeEnabled(true) must flip the bit")

	sess.SetTerminalChromeEnabled(false)
	require.False(t, sess.terminalChromeIsEnabled(),
		"SetTerminalChromeEnabled(false) must restore the bit")
}

// TestAttach_EmitsITermBadgeOnEntry is the structural counterpart of the
// existing #618 scrollback test: pipe os.Stdout, drive Attach + detach, and
// assert that the SetBadgeFormat OSC for the session's DisplayName landed on
// the iTerm2 tty before the user saw any tmux output. This is what closes
// the loop on the external `iterm-badge-sync.sh` poller.
func TestAttach_EmitsITermBadgeOnEntry(t *testing.T) {
	skipIfNoTmuxBinary(t)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is not a terminal (CI/pipe environment); skipping PTY attach test")
	}

	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("AGENTDECK_ITERM_BADGE", "")

	name := SessionPrefix + "ptytest-itermbadge-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	require.NoError(t,
		exec.Command("tmux", "new-session", "-d", "-s", name, "bash").Run(),
		"failed to create test session %s", name,
	)
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	oldStdout := os.Stdout
	os.Stdout = w

	const displayTitle = "badge-eval-fixture"
	// Direct struct literal bypasses NewSession, so terminalChromeEnabled
	// would otherwise default to the zero value (false) — which is also
	// the constructor / upstream-config default now that the feature is
	// opt-in. Set true here to mirror what an opted-in user's session
	// looks like at attach time.
	sess := &Session{Name: name, DisplayName: displayTitle, terminalChromeEnabled: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attachDone := make(chan error, 1)
	go func() { attachDone <- sess.Attach(ctx, 0x11) }()

	time.Sleep(300 * time.Millisecond)
	require.NoError(t,
		exec.Command("tmux", "send-keys", "-t", name, "C-q", "").Run(),
		"failed to send detach key",
	)

	select {
	case attachErr := <-attachDone:
		os.Stdout = oldStdout
		w.Close()
		require.NoError(t, attachErr, "Attach returned error after detach")
	case <-time.After(4 * time.Second):
		cancel()
		os.Stdout = oldStdout
		w.Close()
		t.Fatal("Attach did not return after detach key was sent")
	}

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	captured := buf.String()

	wantSet := "\x1b]1337;SetBadgeFormat=" + base64.StdEncoding.EncodeToString([]byte(displayTitle)) + "\a"
	require.Contains(t, captured, wantSet,
		"Attach() must emit OSC 1337;SetBadgeFormat=<base64(DisplayName)>BEL on entry; captured: %q", captured)

	wantClear := "\x1b]1337;SetBadgeFormat=\a"
	require.Contains(t, captured, wantClear,
		"cleanupAttach() must emit OSC 1337;SetBadgeFormat=BEL (empty payload) to clear the badge on detach; captured: %q", captured)

	setIdx := bytes.Index(buf.Bytes(), []byte(wantSet))
	clearIdx := bytes.LastIndex(buf.Bytes(), []byte(wantClear))
	require.Less(t, setIdx, clearIdx,
		"badge SET must precede badge CLEAR within a single Attach lifecycle; set=%d clear=%d", setIdx, clearIdx)
}
