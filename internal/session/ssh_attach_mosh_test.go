package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/terminal"
	"github.com/creack/pty"
)

func TestRemoteConfigTransport(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "ssh"}, {"ssh", "ssh"}, {" Mosh ", "mosh"}, {"telnet", "telnet"},
	} {
		if got := (RemoteConfig{Transport: tc.in}).GetTransport(); got != tc.want {
			t.Errorf("GetTransport(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	r := NewSSHRunner("box", RemoteConfig{Host: "h", Transport: "MOSH", MoshServer: " /opt/homebrew/bin/mosh-server "})
	if r.transport != RemoteTransportMosh || r.moshServer != "/opt/homebrew/bin/mosh-server" {
		t.Fatalf("runner transport=%q moshServer=%q", r.transport, r.moshServer)
	}
}

func TestMoshAttachArgs(t *testing.T) {
	r := &SSHRunner{Host: "me@box", AgentDeckPath: "/bin/agent deck", Profile: "work ' quoted", moshServer: "/opt/homebrew/bin/mosh-server"}
	got := r.moshAttachArgs("session", "attach", "id ' x")
	want := terminal.MoshArgs("me@box", "/opt/homebrew/bin/mosh-server",
		[]string{"/bin/agent deck", "-p", "work ' quoted", "session", "attach", "id ' x"})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q\nwant   %q", got, want)
	}
	// The ssh path is unchanged by the transport fields.
	if args := r.buildAttachArgs("id"); args[0] != "-tt" || !strings.HasPrefix(args[len(args)-1], "env TERM=") {
		t.Fatalf("ssh attach args = %q", args)
	}
}

func TestAttachInteractiveRejectsUnusableTransport(t *testing.T) {
	r := &SSHRunner{Host: "box", name: "lab", transport: "telnet"}
	if err := r.attachInteractive("session", "attach", "id"); err == nil || !strings.Contains(err.Error(), `unknown transport "telnet"`) {
		t.Fatalf("unknown transport err = %v", err)
	}

	t.Setenv("PATH", t.TempDir())
	r.transport = RemoteTransportMosh
	if err := r.attachInteractive("session", "attach", "id"); err == nil || !strings.Contains(err.Error(), "mosh is not installed") {
		t.Fatalf("missing mosh err = %v", err)
	}
}

// A mosh remote that cannot start mosh-server attaches over ssh rather than
// failing with mosh's bootstrap errors; one that can attaches over mosh.
func TestAttachCommandFallsBackToSSHWithoutMoshServer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mosh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var probed string
	probeErr := errors.New("exit status 1")
	r := &SSHRunner{Host: "me@box", AgentDeckPath: "agent-deck", name: "lab", transport: RemoteTransportMosh, moshServer: "/opt/mosh-server",
		remoteExecFn: func(_ context.Context, cmd string, _ []byte) ([]byte, error) {
			probed = cmd
			return nil, probeErr
		}}

	cmd, transport, err := r.attachCommand("session", "attach", "id")
	if err != nil {
		t.Fatal(err)
	}
	if transport != RemoteTransportSSH || filepath.Base(cmd.Path) != "ssh" || cmd.Args[1] != "-tt" {
		t.Fatalf("missing mosh-server: transport %q, argv %q", transport, cmd.Args)
	}
	if probed != "command -v '/opt/mosh-server'" {
		t.Fatalf("probe ran %q", probed)
	}

	probeErr = nil
	cmd, transport, err = r.attachCommand("session", "attach", "id")
	if err != nil {
		t.Fatal(err)
	}
	if transport != RemoteTransportMosh || cmd.Path != filepath.Join(dir, "mosh") {
		t.Fatalf("mosh-server present: transport %q, path %q", transport, cmd.Path)
	}
}

// startAttachFixture runs script in a PTY the way runRemoteAttach runs the
// transport, and returns the pieces stopRemoteAttach consumes.
func startAttachFixture(t *testing.T, script string) (*exec.Cmd, *os.File, chan error, chan struct{}) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buf := make([]byte, 256)
		for {
			if _, err := ptmx.Read(buf); err != nil {
				return
			}
		}
	}()
	cmdDone := make(chan error, 1)
	go func() { cmdDone <- cmd.Wait() }()
	return cmd, ptmx, cmdDone, outputDone
}

func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s was never written", path)
	return ""
}

func TestStopRemoteAttachMoshQuitsGracefully(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "signal")
	ready := filepath.Join(dir, "ready")
	// Stands in for mosh-client: SIGTERM starts a shutdown that takes a
	// moment and must still be able to use its terminal.
	cmd, ptmx, cmdDone, outputDone := startAttachFixture(t,
		`trap 'sleep 0.2; echo bye; echo term > "`+marker+`"; exit 0' TERM; echo up > "`+ready+`"; while :; do sleep 0.05; done`)
	waitForFile(t, ready)

	output := &attachOutput{w: &strings.Builder{}}
	start := time.Now()
	if !stopRemoteAttach(cmd, ptmx, cmdDone, outputDone, output, RemoteTransportMosh, false, terminal.MoshQuitTimeout) {
		t.Fatal("mosh detach did not hand the PTY to a background quit")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("detach blocked for %v waiting on the quit", elapsed)
	}
	if got := waitForFile(t, marker); got != "term" {
		t.Fatalf("transport saw %q, want a graceful SIGTERM", got)
	}
	if _, err := output.Write([]byte("late frame")); err != nil || output.w.(*strings.Builder).Len() != 0 {
		t.Fatal("output after detach reached the terminal")
	}
	select {
	case <-outputDone:
	case <-time.After(5 * time.Second):
		t.Fatal("PTY was never closed after the quit")
	}
}

func TestStopRemoteAttachMoshKillsAStuckQuit(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	cmd, ptmx, cmdDone, outputDone := startAttachFixture(t,
		`trap '' TERM; echo up > "`+ready+`"; while :; do sleep 0.05; done`)
	waitForFile(t, ready)

	stopRemoteAttach(cmd, ptmx, cmdDone, outputDone, &attachOutput{w: &strings.Builder{}}, RemoteTransportMosh, false, 100*time.Millisecond)
	select {
	case <-outputDone:
	case <-time.After(5 * time.Second):
		t.Fatal("a transport ignoring SIGTERM was never killed")
	}
}

func TestStopRemoteAttachSSHKillsImmediately(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	cmd, ptmx, cmdDone, outputDone := startAttachFixture(t,
		`trap '' TERM HUP; echo up > "`+ready+`"; while :; do sleep 0.05; done`)
	waitForFile(t, ready)

	if stopRemoteAttach(cmd, ptmx, cmdDone, outputDone, &attachOutput{w: &strings.Builder{}}, RemoteTransportSSH, false, terminal.MoshQuitTimeout) {
		t.Fatal("ssh detach handed off the PTY")
	}
	select {
	case <-cmdDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ssh transport survived detach")
	}
}
