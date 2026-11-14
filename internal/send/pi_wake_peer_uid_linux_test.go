package send

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The pane tree is one guard on the listener; its uid is a second. Here the
// listener's pid is in the pane tree but it listens as another user, so the
// wake is refused before anything is written. Switching the helper's uid needs
// root, so the test runs only as root (the CI containers are).
func TestPiWakeSocket_PeerOfAnotherUserIsRefused(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root to run the helper listener as another user")
	}
	const other = 65534 // nobody

	// The helper must be able to exec a copy of this binary and reach the
	// socket directory as that user.
	base, err := os.MkdirTemp("/tmp", "pwu")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(base, "helper.test")
	copyExecutable(t, os.Args[0], bin)
	dir := filepath.Join(base, "pi")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, other, other); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "w.sock")

	cmd := exec.Command(bin, "-test.run=^TestPiWakeHelperListener$", "-test.count=1")
	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), piWakeHelperEnv+"="+path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: other, Gid: other}}
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
	// The files pass the filesystem vet as ours; only the listener's own
	// credentials still say another user.
	for _, p := range []string{path, dir} {
		if err := os.Chown(p, 0, 0); err != nil {
			t.Fatal(err)
		}
	}

	child := cmd.Process.Pid
	_, err = SendPiWake(piWakeContext(t), PiWakeTarget{Path: path}, piWakeNonce, "wake", piWakeIDs,
		PiWakeOptions{PanePIDs: map[int]bool{child: true}})
	if got := piReasonOf(t, err); got != "peer_not_in_pane_tree" {
		t.Errorf("listener in the pane tree but running as uid %d: reason = %q (err %v), want peer_not_in_pane_tree", other, got, err)
	}
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	// The create mode passes through the umask; the helper's user must be
	// able to execute the copy whatever the umask is.
	if err := os.Chmod(to, 0o755); err != nil {
		t.Fatal(err)
	}
}
