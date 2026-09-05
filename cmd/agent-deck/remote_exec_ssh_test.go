package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// startParitySSH runs an authenticated SSH server fixture for the real OpenSSH
// client. Only the server's command process sees the remote HOME. No sshd
// installation, host sockets, real credentials or external network are needed.
func startParitySSH(t *testing.T, remoteHome, binDir string) {
	t.Helper()
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client required")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) != string(clientKey.Marshal()) {
			return nil, fmt.Errorf("unknown test client")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() { listener.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					conn.Close()
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						incoming.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer channel.Close()
						for req := range requests {
							if req.Type != "exec" {
								req.Reply(false, nil)
								continue
							}
							var payload struct{ Command string }
							if ssh.Unmarshal(req.Payload, &payload) != nil {
								req.Reply(false, nil)
								return
							}
							req.Reply(true, nil)
							cmd := exec.Command("sh", "-c", payload.Command)
							cmd.Dir = remoteHome
							for _, kv := range os.Environ() {
								key := strings.SplitN(kv, "=", 2)[0]
								if key == "PATH" || key == "HOME" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "AGENTDECK_") || strings.HasPrefix(key, "TMUX") {
									continue
								}
								cmd.Env = append(cmd.Env, kv)
							}
							cmd.Env = append(cmd.Env, "HOME="+remoteHome, "PATH="+binDir+":"+os.Getenv("PATH"))
							cmd.Stdin, cmd.Stdout, cmd.Stderr = channel, channel, channel.Stderr()
							status := uint32(0)
							if err := cmd.Run(); err != nil {
								status = 1
								if exitErr, ok := err.(*exec.ExitError); ok {
									status = uint32(exitErr.ExitCode())
								}
							}
							channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
							return
						}
					}()
				}
			}()
		}
	}()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, data, 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	privateBlock, err := ssh.MarshalPrivateKey(private, "parity test")
	if err != nil {
		t.Fatal(err)
	}
	keyFile := write("identity", pem.EncodeToMemory(privateBlock))
	port := listener.Addr().(*net.TCPAddr).Port
	known := write("known_hosts", []byte(fmt.Sprintf("[127.0.0.1]:%d %s", port, ssh.MarshalAuthorizedKey(signer.PublicKey()))))
	sshConfig := write("config", []byte("Host test-host\n HostName 127.0.0.1\n Port "+strconv.Itoa(port)+"\n IdentityFile "+keyFile+"\n UserKnownHostsFile "+known+"\n StrictHostKeyChecking yes\n IdentitiesOnly yes\n ControlMaster no\n ControlPath none\n"))
	// Explicit command-line options disable persistent control masters so no SSH
	// connection outlives this fixture; production host-key options still apply.
	write("ssh", []byte(fmt.Sprintf("#!/bin/sh\nexec '%s' -F '%s' -o ControlMaster=no -o ControlPath=none \"$@\"\n", realSSH, sshConfig)))
}
