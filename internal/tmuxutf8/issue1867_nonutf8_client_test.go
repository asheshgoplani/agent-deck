package tmuxutf8_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// spinnerGlyph is U+280B BRAILLE PATTERN DOTS-1-2-4 (bytes e2 a0 8b), one of
// the frames Claude Code writes into the pane title while a tool call is in
// flight. AnalyzePaneTitle treats any U+2800–U+28FF rune as TitleStateWorking,
// and that is the ONLY reliable mid-tool-call "running" signal there is — the
// activity timestamp cannot distinguish real work from a status-bar repaint.
const spinnerGlyph = "⠋"

const spinnerTitle = spinnerGlyph + " Working on refactor"

// nonUTF8Locale blanks the three variables tmux consults to decide whether a
// client can be sent UTF-8 (LC_ALL, then LC_CTYPE, then LANG). tmux treats an
// empty value exactly like an unset one, so this reproduces what systemd,
// launchd and a bare container hand every service they start — verified
// byte-for-byte against the `env -u LANG -u LC_ALL -u LC_CTYPE` form in the
// issue.
//
// t.Setenv is used rather than os.Unsetenv so the restore is automatic and the
// runtime refuses the test if it ever gains t.Parallel.
func nonUTF8Locale(t *testing.T) {
	t.Helper()
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "")
}

// utf8Locale is the control condition: a client whose locale plainly announces
// UTF-8, i.e. an ordinary interactive shell.
func utf8Locale(t *testing.T) {
	t.Helper()
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C.UTF-8")
}

// startSpinnerSession boots a private tmux server, creates one detached
// session running `sleep`, and stamps the braille spinner into its pane title.
// It returns the socket name and the tmux session name.
//
// Setup deliberately uses raw exec.Command with an explicit `-L` rather than
// the tmux factory: the factory is the code under test, and the setup path must
// behave identically at the merge-base and at HEAD or the test would not be
// measuring anything. It is safe because TestMain has already repointed
// TMUX_TMPDIR and cleared $TMUX, and because every command here names its
// socket — there is no bare kill-server anywhere in this file.
func startSpinnerSession(t *testing.T) (socket, session string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not available")
	}

	socket = fmt.Sprintf("ad1867-%d-%s", os.Getpid(), strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())))
	session = "agentdeck_i1867"

	run := func(args ...string) {
		t.Helper()
		full := append([]string{"-L", socket}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %s: %v\n%s", strings.Join(full, " "), err, out)
		}
	}

	t.Cleanup(func() {
		// Socket-scoped. NEVER a bare kill-server: on the default socket that
		// ends every tmux session on the machine.
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})

	run("new-session", "-d", "-s", session, "sleep", "300")
	run("select-pane", "-t", session, "-T", spinnerTitle)

	// select-pane is synchronous against the server, but the pane's program
	// still has to exist before list-panes reports it; poll briefly rather than
	// sleeping a fixed amount.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("tmux", "-L", socket, "has-session", "-t", session).Run() == nil {
			return socket, session
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("tmux session %q never became visible on socket %q", session, socket)
	return "", ""
}

// rawPaneTitle reads the pane title with a hand-built argv that does NOT carry
// `-u`, i.e. exactly what agent-deck used to send. It is the test's probe for
// whether this platform actually downgrades.
func rawPaneTitle(t *testing.T, socket, session string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "list-panes", "-t", session, "-F", "#{pane_title}").Output()
	if err != nil {
		t.Fatalf("raw list-panes: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// readPaneTitleThroughAgentDeck drives the real production read path:
// RefreshPaneInfoCache issues the `list-panes -a -F …` that feeds every status
// decision, and GetCachedPaneInfo is what (*Session).GetStatus consults.
func readPaneTitleThroughAgentDeck(t *testing.T, socket, session string) tmux.PaneInfo {
	t.Helper()
	tmux.SetDefaultSocketName(socket)
	t.Cleanup(func() { tmux.SetDefaultSocketName("") })

	tmux.RefreshPaneInfoCache()
	info, ok := tmux.GetCachedPaneInfo(session)
	if !ok {
		t.Fatalf("RefreshPaneInfoCache did not cache session %q", session)
	}
	return info
}

// TestIssue1867_NonUTF8Client_StatusStillReadsSpinner is the regression test
// for #1867.
//
// With no UTF-8 locale, tmux does not set CLIENT_UTF8 on the client and runs
// every message it prints to it through utf8_sanitize(), which rewrites each
// non-ASCII byte to "_". The server's copy of the title is untouched, so the
// corruption is invisible to everything except the parser: agent-deck reads
// "_ Working on refactor", AnalyzePaneTitle finds no braille rune, the title
// fast path in GetStatus never fires, and a session that is plainly running is
// reported idle.
//
// This test fails at the merge-base with a "_"-mangled title and passes once
// the argv factory emits tmux's global `-u`.
func TestIssue1867_NonUTF8Client_StatusStillReadsSpinner(t *testing.T) {
	nonUTF8Locale(t)
	socket, session := startSpinnerSession(t)

	// Precondition: prove THIS platform+tmux actually downgrades under this
	// environment. macOS does not reproduce (per the issue's measured matrix),
	// and a box whose locale leaked back in would make every assertion below
	// pass vacuously. Skipping here is honest; asserting would be a lie on
	// darwin.
	raw := rawPaneTitle(t, socket, session)
	if strings.Contains(raw, spinnerGlyph) {
		t.Skipf("tmux on this platform does not downgrade non-ASCII for a non-UTF-8 client "+
			"(raw title without -u came back as %q / % x) — #1867 cannot manifest here", raw, raw)
	}
	if !strings.HasPrefix(raw, "_") {
		t.Fatalf("unexpected raw title %q (% x): expected either the glyph or the '_' downgrade", raw, raw)
	}
	t.Logf("confirmed downgrade without -u: %q (% x)", raw, raw)

	info := readPaneTitleThroughAgentDeck(t, socket, session)

	if !strings.Contains(info.Title, spinnerGlyph) {
		t.Errorf("agent-deck read a mangled pane title from a non-UTF-8 client\n"+
			" got:  %q (% x)\n want to contain U+280B (e2 a0 8b)\n"+
			"tmux downgraded every non-ASCII byte to '_' because the client carried no UTF-8 locale (#1867).",
			info.Title, info.Title)
	}

	if state := tmux.AnalyzePaneTitle(info.Title, info.CurrentCommand); state != tmux.TitleStateWorking {
		t.Errorf("AnalyzePaneTitle(%q) = %v, want TitleStateWorking — the session is running and the "+
			"spinner is the only signal that says so", info.Title, state)
	}

	s := &tmux.Session{Name: session, SocketName: socket}
	status, err := s.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status != "active" {
		t.Errorf("GetStatus() = %q, want \"active\" — a session mid-tool-call reported as %q is exactly "+
			"the user-visible symptom of #1867", status, status)
	}
}

// TestIssue1867_UTF8Client_StatusUnchanged is the no-regression half: in an
// ordinary UTF-8 environment — where the bug never existed — the read path must
// still return the glyph and still report the session running. `-u` is a no-op
// for a client whose locale already announces UTF-8, and this pins that.
func TestIssue1867_UTF8Client_StatusUnchanged(t *testing.T) {
	utf8Locale(t)
	socket, session := startSpinnerSession(t)

	if raw := rawPaneTitle(t, socket, session); !strings.Contains(raw, spinnerGlyph) {
		t.Fatalf("a UTF-8 client should never see a downgrade; raw title was %q (% x)", raw, raw)
	}

	info := readPaneTitleThroughAgentDeck(t, socket, session)
	if info.Title != spinnerTitle {
		t.Errorf("pane title round-trip broken in a UTF-8 environment\n got:  %q (% x)\n want: %q",
			info.Title, info.Title, spinnerTitle)
	}
	if state := tmux.AnalyzePaneTitle(info.Title, info.CurrentCommand); state != tmux.TitleStateWorking {
		t.Errorf("AnalyzePaneTitle(%q) = %v, want TitleStateWorking", info.Title, state)
	}

	s := &tmux.Session{Name: session, SocketName: socket}
	status, err := s.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status != "active" {
		t.Errorf("GetStatus() = %q, want \"active\"", status)
	}
}
