//go:build !windows

package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestInsertTextToPrimaryWindow_TypesLiteralWithoutEnter records the tmux
// argv: one literal send-keys into the managed first window, and no Enter.
func TestInsertTextToPrimaryWindow_TypesLiteralWithoutEnter(t *testing.T) {
	calls := recordTransport(t)
	s := &Session{Name: "agentdeck_ref_probe"}
	line := `[ref] session "A" (abc) · /p · status idle `
	if err := s.InsertTextToPrimaryWindow(line); err != nil {
		t.Fatalf("InsertTextToPrimaryWindow: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want exactly one tmux call, got %d: %v", len(*calls), argvs(*calls))
	}
	argv := (*calls)[0].argv
	want := []string{"send-keys", "-l", "-t", "agentdeck_ref_probe:^", "--", line}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	for _, c := range *calls {
		for _, a := range c.argv {
			if a == "Enter" || a == "C-m" || a == "KPEnter" {
				t.Fatalf("insert pressed a submit key: %q", c.argv)
			}
		}
	}
}

// TestInsertTextToPrimaryWindow_VimModeEntersInsertModeFirst: a vim-mode
// composer gets Escape then i before the text (never Enter).
func TestInsertTextToPrimaryWindow_VimModeEntersInsertModeFirst(t *testing.T) {
	calls := recordTransport(t)
	s := &Session{Name: "agentdeck_ref_vim", VimMode: true}
	if err := s.InsertTextToPrimaryWindow("hello "); err != nil {
		t.Fatalf("InsertTextToPrimaryWindow: %v", err)
	}
	got := argvs(*calls)
	want := []string{
		"send-keys -t agentdeck_ref_vim:^ Escape",
		"send-keys -t agentdeck_ref_vim:^ i",
		"send-keys -l -t agentdeck_ref_vim:^ -- hello ",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestInsertTextToPrimaryWindowChecked_GuardRunsBeforeEveryBatch: the guard
// runs before the vim Escape, the i and the text; a refusal stops the insert
// right there and is returned as is (session ref's menu guard, ASK-111).
func TestInsertTextToPrimaryWindowChecked_GuardRunsBeforeEveryBatch(t *testing.T) {
	errMenu := errors.New("menu open")
	for refuseAt := 1; refuseAt <= 4; refuseAt++ {
		calls := recordTransport(t)
		s := &Session{Name: "agentdeck_ref_guard", VimMode: true}
		n := 0
		err := s.InsertTextToPrimaryWindowChecked("hello ", func() error {
			n++
			if n == refuseAt {
				return errMenu
			}
			return nil
		})
		all := []string{
			"send-keys -t agentdeck_ref_guard:^ Escape",
			"send-keys -t agentdeck_ref_guard:^ i",
			"send-keys -l -t agentdeck_ref_guard:^ -- hello ",
		}
		want := all[:min(refuseAt-1, 3)]
		got := argvs(*calls)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("refuse at guard call %d: calls =\n%s\nwant\n%s", refuseAt, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if refuseAt <= 3 && !errors.Is(err, errMenu) {
			t.Fatalf("refuse at %d: err = %v, want the guard's error", refuseAt, err)
		}
		if refuseAt == 4 && (err != nil || n != 3) {
			t.Fatalf("guard never refused: err=%v guard calls=%d, want nil and 3", err, n)
		}
	}
}

// TestInsertTextToPrimaryWindow_RefusesLineBreaks: CR or LF would submit in
// a shell or a non-bracketed composer, so nothing is typed at all.
func TestInsertTextToPrimaryWindow_RefusesLineBreaks(t *testing.T) {
	for _, text := range []string{"a\nb", "a\rb", "trailing\n", "x\r\n"} {
		calls := recordTransport(t)
		s := &Session{Name: "agentdeck_ref_nl"}
		err := s.InsertTextToPrimaryWindow(text)
		if !errors.Is(err, ErrInsertHasLineBreak) {
			t.Fatalf("%q: err = %v, want ErrInsertHasLineBreak", text, err)
		}
		if len(*calls) != 0 {
			t.Fatalf("%q: typed despite refusal: %v", text, argvs(*calls))
		}
	}
}

// TestInsertTextToPrimaryWindow_RealPaneNotSubmitted runs on a private tmux
// server (-L session-ref-test-<pid>) with `cat` in the pane. Typed text is
// echoed once by the tty; a submitted line would be printed a second time by
// cat. The positive control presses Enter afterwards and sees the second copy.
func TestInsertTextToPrimaryWindow_RealPaneNotSubmitted(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	socket := fmt.Sprintf("session-ref-test-%d", os.Getpid())
	name := "agentdeck_session_ref_probe"
	if out, err := exec.Command("tmux", "-L", socket, "-f", "/dev/null", "new-session", "-d", "-s", name, "-x", "200", "-y", "20", "cat").CombinedOutput(); err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", name).Run() })

	s := &Session{Name: name, SocketName: socket}
	marker := "REFMARK-7d1e"
	if err := s.InsertTextToPrimaryWindow("[ref] " + marker + " read it "); err != nil {
		t.Fatalf("InsertTextToPrimaryWindow: %v", err)
	}
	count := func() int {
		out, err := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", name).CombinedOutput()
		if err != nil {
			t.Fatalf("capture-pane: %v: %s", err, out)
		}
		return strings.Count(string(out), marker)
	}
	waitFor := func(want int) int {
		deadline := time.Now().Add(3 * time.Second)
		n := count()
		for n != want && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			n = count()
		}
		return n
	}
	if n := waitFor(1); n != 1 {
		t.Fatalf("after insert the marker shows %d times, want 1 (typed, not submitted)", n)
	}
	// Give a stray Enter time to arrive, then re-check: still only the echo.
	time.Sleep(300 * time.Millisecond)
	if n := count(); n != 1 {
		t.Fatalf("marker shows %d times: the inserted text was submitted", n)
	}
	// Positive control: an explicit Enter makes cat print the line again.
	if err := exec.Command("tmux", "-L", socket, "send-keys", "-t", name, "Enter").Run(); err != nil {
		t.Fatalf("send Enter: %v", err)
	}
	if n := waitFor(2); n != 2 {
		t.Fatalf("control: after Enter the marker shows %d times, want 2", n)
	}
}

func argvs(calls []*tmuxCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, strings.Join(c.argv, " "))
	}
	return out
}
