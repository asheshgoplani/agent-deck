package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// nativeSSHProxy can blackhole existing streams without disconnecting them.
// Faults affect only this fixture's loopback connections.
type nativeSSHProxy struct {
	mu          sync.Mutex
	paused      bool
	stopped     bool
	connections map[net.Conn]bool
	changed     chan struct{}
	listener    net.Listener
	wg          sync.WaitGroup
}

func (p *nativeSSHProxy) pause(paused bool) {
	p.mu.Lock()
	p.paused = paused
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
}

func (p *nativeSSHProxy) drop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.connections {
		_ = c.Close()
	}
}

func (p *nativeSSHProxy) copy(dst, src net.Conn) {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			for {
				p.mu.Lock()
				paused, stopped, changed := p.paused, p.stopped, p.changed
				p.mu.Unlock()
				if stopped {
					return
				}
				if !paused {
					break
				}
				<-changed
			}
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func startNativeSSH(t *testing.T, remoteHome, binDir string) *nativeSSHProxy {
	t.Helper()
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	sshd := os.Getenv("NATIVE_SSHD")
	if sshd == "" {
		sshd = "/usr/sbin/sshd"
	}
	if _, err := os.Stat(sshd); err != nil {
		if os.IsNotExist(err) && os.Getenv("NATIVE_SSHD") == "" {
			nativeSSHMissingTool(t, sshd)
		}
		t.Fatalf("real sshd required: %v", err)
	}
	authorizedCommand := "/usr/bin/cat"
	if runtime.GOOS == "darwin" {
		authorizedCommand = "/bin/cat"
	}
	if _, err := os.Stat(authorizedCommand); err != nil {
		if os.IsNotExist(err) {
			nativeSSHMissingTool(t, authorizedCommand)
		}
		t.Fatalf("authorized key command required: %v", err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, data, 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	makeKey := func(name string) (string, ssh.PublicKey) {
		t.Helper()
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key, err := ssh.NewPublicKey(public)
		if err != nil {
			t.Fatal(err)
		}
		block, err := ssh.MarshalPrivateKey(private, "native SSH fixture")
		if err != nil {
			t.Fatal(err)
		}
		return write(name, pem.EncodeToMemory(block)), key
	}
	hostFile, hostKey := makeKey("host-key")
	clientFile, clientKey := makeKey("client-key")
	authorized := write("authorized_keys", ssh.MarshalAuthorizedKey(clientKey))
	// Reserve a loopback port, then hand it to sshd. Readiness below detects bind failures.
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target := reserve.Addr().String()
	port := reserve.Addr().(*net.TCPAddr).Port
	_ = reserve.Close()
	wrapper := write("remote-command", []byte("#!/bin/sh\nunset XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME TMUX AGENTDECK_PROFILE CLAUDE_CONFIG_DIR\nexport HOME="+quote(remoteHome)+"\nexport PATH="+quote(os.Getenv("PATH"))+"\ncd "+quote(remoteHome)+" || exit 1\nexec /bin/sh -c \"$SSH_ORIGINAL_COMMAND\"\n"))
	// Read only the disposable key without requiring /tmp ancestry to pass StrictModes.
	// sshd validates that the absolute command is root-owned and not publicly writable.
	config := write("sshd_config", []byte(fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\nAuthorizedKeysFile none\nAuthorizedKeysCommand %s %s\nAuthorizedKeysCommandUser %s\nStrictModes yes\nUsePAM no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nPermitUserEnvironment no\nAllowUsers %s\nForceCommand %s\nLogLevel VERBOSE\n", port, hostFile, filepath.Join(binDir, "sshd.pid"), authorizedCommand, authorized, account.Username, account.Username, wrapper)))
	logFile, err := os.Create(filepath.Join(binDir, "sshd.log"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(sshd, "-D", "-e", "-f", config)
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	sshdDone := make(chan error, 1)
	go func() { sshdDone <- command.Wait() }()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		select {
		case <-sshdDone:
		case <-time.After(3 * time.Second):
			t.Error("sshd cleanup timed out")
		}
		_ = logFile.Close()
		nativeRetainFile(t, "sshd.log", filepath.Join(binDir, "sshd.log"))
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", target, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sshd not listening: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &nativeSSHProxy{connections: make(map[net.Conn]bool), changed: make(chan struct{}), listener: listener}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			server, err := net.DialTimeout("tcp", target, time.Second)
			if err != nil {
				_ = client.Close()
				continue
			}
			p.mu.Lock()
			if p.stopped {
				p.mu.Unlock()
				_ = client.Close()
				_ = server.Close()
				return
			}
			p.connections[client], p.connections[server] = true, true
			p.mu.Unlock()
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				done := make(chan struct{})
				go func() { p.copy(server, client); _ = server.Close(); _ = client.Close(); close(done) }()
				p.copy(client, server)
				_ = client.Close()
				_ = server.Close()
				<-done
				p.mu.Lock()
				delete(p.connections, client)
				delete(p.connections, server)
				p.mu.Unlock()
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		p.mu.Lock()
		p.stopped = true
		close(p.changed)
		p.mu.Unlock()
		p.drop()
		done := make(chan struct{})
		go func() { p.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("proxy cleanup timed out")
		}
	})
	proxyPort := listener.Addr().(*net.TCPAddr).Port
	known := write("known_hosts", []byte(fmt.Sprintf("[127.0.0.1]:%d %s", proxyPort, ssh.MarshalAuthorizedKey(hostKey))))
	clientConfig := write("ssh_config", []byte("Host test-host\n HostName 127.0.0.1\n User "+account.Username+"\n Port "+strconv.Itoa(proxyPort)+"\n IdentityFile "+clientFile+"\n UserKnownHostsFile "+known+"\n StrictHostKeyChecking yes\n IdentitiesOnly yes\n"))
	write("ssh", []byte("#!/bin/sh\nif [ \"$NATIVE_SSH_MULTIPLEX\" = true ]; then\nexec "+quote(realSSH)+" -F "+quote(clientConfig)+" \"$@\"\nfi\nexec "+quote(realSSH)+" -F "+quote(clientConfig)+" -o ControlMaster=no -o ControlPath=none \"$@\"\n"))
	return p
}

func nativeRetainFile(t *testing.T, name, source string) {
	t.Helper()
	dir := os.Getenv("NATIVE_SSH_RECEIPT_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Error(err)
		return
	}
	input, err := os.Open(source)
	if err != nil {
		t.Error(err)
		return
	}
	defer input.Close()
	output, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Error(err)
		return
	}
	defer output.Close()
	if _, err := io.Copy(output, input); err != nil {
		t.Error(err)
	}
}

// Ordinary developer runs may lack SSH server tools. Acceptance runs opt in
// explicitly and also verify that this test ran, so a skip cannot certify them.
func nativeSSHMissingTool(t *testing.T, tool string) {
	t.Helper()
	if os.Getenv("NATIVE_SSH_REQUIRED") == "1" {
		t.Fatalf("native SSH acceptance requires %s", tool)
	}
	t.Skipf("native SSH integration requires %s", tool)
}
