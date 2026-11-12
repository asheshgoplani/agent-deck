package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// SendBackspaces is the only keystroke send.WithdrawTyped uses to take back
// text a failed send typed. Needs the new method; fails to compile on base.

func TestSendBackspaces_SendsNBSpaceArgsWithoutDashN(t *testing.T) {
	calls := recordKeySender(t)
	s := &Session{Name: "agentdeck_bs_argv"}
	if err := s.SendBackspaces(3); err != nil {
		t.Fatalf("SendBackspaces: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0] != "send-keys -t agentdeck_bs_argv BSpace BSpace BSpace" {
		t.Fatalf("calls = %q, want one send-keys with three BSpace args (no -N: old tmux)", *calls)
	}
}

func TestSendBackspaces_NonPositiveIsNoop(t *testing.T) {
	calls := recordKeySender(t)
	s := &Session{Name: "agentdeck_bs_zero"}
	for _, n := range []int{0, -1} {
		if err := s.SendBackspaces(n); err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("calls = %q, want none", *calls)
	}
}

// Real tmux on a private -L socket (TMUX unset), `cat` in the pane: typed text
// is echoed by the tty; BSpace erases it in the line discipline.
func TestSendBackspaces_RealPaneErasesTypedText(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	t.Setenv("TMUX", "")
	_ = os.Unsetenv("TMUX")
	socket := fmt.Sprintf("send-bs-test-%d", os.Getpid())
	name := "agentdeck_send_bs_probe"
	if out, err := exec.Command("tmux", "-L", socket, "-f", "/dev/null", "new-session", "-d", "-s", name, "-x", "200", "-y", "20", "cat").CombinedOutput(); err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", name).Run() })

	s := &Session{Name: name, SocketName: socket}
	if out, err := exec.Command("tmux", "-L", socket, "send-keys", "-t", name, "-l", "abcdef").CombinedOutput(); err != nil {
		t.Fatalf("type: %v: %s", err, out)
	}
	pane := func() string {
		out, err := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", name).CombinedOutput()
		if err != nil {
			t.Fatalf("capture-pane: %v: %s", err, out)
		}
		return string(out)
	}
	waitFor := func(want string) string {
		deadline := time.Now().Add(3 * time.Second)
		p := pane()
		for !strings.Contains(p, want) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			p = pane()
		}
		return p
	}
	if p := waitFor("abcdef"); !strings.Contains(p, "abcdef") {
		t.Fatalf("typed text never showed: %q", p)
	}
	if err := s.SendBackspaces(3); err != nil {
		t.Fatalf("SendBackspaces: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	p := pane()
	for strings.Contains(p, "abcdef") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		p = pane()
	}
	if strings.Contains(p, "abcdef") || !strings.Contains(p, "abc") {
		t.Fatalf("after 3 BSpace the line should read abc, pane = %q", p)
	}
}
