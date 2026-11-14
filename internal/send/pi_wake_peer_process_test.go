package send

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// piWakeHelperEnv names the socket a helper process serves; unset, the helper
// test is a no-op.
const piWakeHelperEnv = "AGENTDECK_TEST_PI_WAKE_HELPER_SOCK"

// TestPiWakeHelperListener is not a test of its own: run as a child process of
// TestPiWakeSocket_PeerCheckUsesTheListenersPid, it serves a wake socket from a
// pid other than the client's. It answers every frame submitted, echoing the
// frame's id, until its stdin closes.
func TestPiWakeHelperListener(t *testing.T) {
	path := os.Getenv(piWakeHelperEnv)
	if path == "" {
		return
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				var frame struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(line, &frame)
				_, _ = conn.Write([]byte(`{"id":"` + frame.ID + `","state":"submitted"}` + "\n"))
			}()
		}
	}()
	_, _ = os.Stdout.WriteString("ready\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	_ = ln.Close()
}

// Every other listener in these tests runs in the test process, so its peer
// pid is the client's own and a peerPID that returned the caller's pid would
// pass them. Here the listener is a child process: the wake is accepted when
// the pane tree holds the child's pid and refused when it holds only ours.
func TestPiWakeSocket_PeerCheckUsesTheListenersPid(t *testing.T) {
	dir := filepath.Join(piWakeShortDir(t), "pi")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "w.sock")

	cmd := exec.Command(os.Args[0], "-test.run=^TestPiWakeHelperListener$", "-test.count=1")
	cmd.Env = append(os.Environ(), piWakeHelperEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == "ready" {
				ready <- true
				break
			}
		}
		for sc.Scan() {
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("the helper listener exited before it was ready")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the helper listener never became ready")
	}
	child := cmd.Process.Pid
	if child == os.Getpid() {
		t.Fatal("test setup: the helper runs in this process")
	}

	res, err := SendPiWake(piWakeContext(t), PiWakeTarget{Path: path}, piWakeNonce, "wake", piWakeIDs,
		PiWakeOptions{PanePIDs: map[int]bool{child: true}})
	if err != nil || res.State != PiWakeSubmitted {
		t.Errorf("pane tree holds the listener's pid %d: got %+v, %v; want submitted", child, res, err)
	}

	_, err = SendPiWake(piWakeContext(t), PiWakeTarget{Path: path}, piWakeNonce, "wake", piWakeIDs,
		PiWakeOptions{PanePIDs: map[int]bool{os.Getpid(): true}})
	if got := piReasonOf(t, err); got != "peer_not_in_pane_tree" {
		t.Errorf("pane tree holds only the client's pid: reason = %q, want peer_not_in_pane_tree", got)
	}
}
