package harness

import (
	"os/exec"
	"testing"

	"github.com/creack/pty"
)

func TestPTYSessionCloseReapsProcess(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	f, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	p := &PTYSession{t: t, cmd: cmd, f: f, waitDone: make(chan struct{})}
	p.drainWG.Add(1)
	go p.drain()
	go p.wait()

	p.Close()

	if cmd.ProcessState == nil {
		t.Fatal("Close returned before the PTY process was reaped")
	}
	select {
	case <-p.waitDone:
	default:
		t.Fatal("Close returned before the process waiter finished")
	}
}
