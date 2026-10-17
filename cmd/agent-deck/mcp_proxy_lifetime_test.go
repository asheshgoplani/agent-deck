package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type mcpProxyFixture struct {
	cmd    *exec.Cmd
	input  *os.File
	output *bufio.Reader
	done   chan error
}

func newMCPProxyFixture(t *testing.T, socket string) *mcpProxyFixture {
	t.Helper()
	in, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out, reader, err := os.Pipe()
	if err != nil {
		_ = in.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	cmd := exec.Command(channelsCLIBinary(t), "mcp-proxy", socket)
	cmd.Env = agentDeckTestEnv(t.TempDir(), nil)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, reader, os.Stderr
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		_ = writer.Close()
		_ = out.Close()
		_ = reader.Close()
		t.Fatal(err)
	}
	_ = in.Close()
	_ = reader.Close()
	f := &mcpProxyFixture{cmd: cmd, input: writer, output: bufio.NewReader(out), done: make(chan error, 1)}
	go func() { f.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = writer.Close()
		_ = out.Close()
		_ = cmd.Process.Kill()
		select {
		case <-f.done:
		case <-time.After(2 * time.Second):
			t.Error("proxy cleanup did not finish")
		}
	})
	return f
}

func mcpProxySocket(t *testing.T) (*net.UnixListener, string) {
	t.Helper()
	// Unix sockets have a small pathname limit, including on macOS.
	dir, err := os.MkdirTemp("", "mcp-life-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "proxy.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener, path
}

func acceptMCPProxy(t *testing.T, listener *net.UnixListener) *net.UnixConn {
	t.Helper()
	if err := listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func exchangeMCPProxy(t *testing.T, f *mcpProxyFixture, conn *net.UnixConn, message string) {
	t.Helper()
	if _, err := fmt.Fprintln(f.input, message); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != message+"\n" {
		t.Fatalf("backend received %q, %v; want %q", line, err, message)
	}
	if _, err := fmt.Fprintln(conn, "reply:"+message); err != nil {
		t.Fatal(err)
	}
	result := make(chan string, 1)
	go func() {
		line, err := f.output.ReadString('\n')
		result <- fmt.Sprintf("%s|%v", line, err)
	}()
	select {
	case got := <-result:
		if want := "reply:" + message + "\n|<nil>"; got != want {
			t.Fatalf("client received %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend reply did not reach client")
	}
}

func assertMCPProxyEOFExit(t *testing.T, f *mcpProxyFixture) {
	t.Helper()
	if err := f.input.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-f.done:
		// Keep the completion available to the cleanup reaper.
		f.done <- err
		if err != nil {
			t.Fatalf("stdin EOF exit: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy still alive after stdin EOF")
	}
}

func TestMCPProxyBidirectionalFlow(t *testing.T) {
	listener, path := mcpProxySocket(t)
	f := newMCPProxyFixture(t, path)
	conn := acceptMCPProxy(t, listener)
	exchangeMCPProxy(t, f, conn, "first")
	exchangeMCPProxy(t, f, conn, "second")
}

func TestMCPProxyStdinEOF(t *testing.T) {
	for _, mode := range []string{"connected", "dial-backoff", "backend-disconnected"} {
		t.Run(mode, func(t *testing.T) {
			listener, path := mcpProxySocket(t)
			if mode == "dial-backoff" {
				_ = listener.Close()
			}
			f := newMCPProxyFixture(t, path)
			if mode != "dial-backoff" {
				conn := acceptMCPProxy(t, listener)
				exchangeMCPProxy(t, f, conn, "before-eof")
				if mode == "backend-disconnected" {
					_ = listener.Close()
					_ = conn.Close()
				}
			} else {
				// Let unsuccessful dials enter the exponential backoff.
				time.Sleep(350 * time.Millisecond)
			}
			assertMCPProxyEOFExit(t, f)
		})
	}
}

func TestMCPProxyReconnectKeepsInput(t *testing.T) {
	listener, path := mcpProxySocket(t)
	f := newMCPProxyFixture(t, path)
	for i := 0; i < 4; i++ {
		conn := acceptMCPProxy(t, listener)
		exchangeMCPProxy(t, f, conn, fmt.Sprintf("request-%d", i))
		_ = conn.Close()
	}
	assertMCPProxyEOFExit(t, f)
}

func TestMCPProxyEOFWithInputBackpressure(t *testing.T) {
	for _, mode := range []string{"dial-backoff", "blocked-backend"} {
		t.Run(mode, func(t *testing.T) {
			listener, path := mcpProxySocket(t)
			if mode == "dial-backoff" {
				_ = listener.Close()
			}
			f := newMCPProxyFixture(t, path)
			if mode == "blocked-backend" {
				conn := acceptMCPProxy(t, listener)
				exchangeMCPProxy(t, f, conn, "before-backpressure")
			}
			written := make(chan error, 1)
			go func() {
				_, err := f.input.Write(bytes.Repeat([]byte("x"), 4<<20))
				written <- err
			}()
			time.Sleep(100 * time.Millisecond)
			select {
			case err := <-written:
				t.Fatalf("fixture did not exercise input backpressure: %v", err)
			default:
			}
			assertMCPProxyEOFExit(t, f)
			select {
			case <-written:
			case <-time.After(time.Second):
				t.Fatal("fixture stdin writer did not stop after close")
			}
		})
	}
}

func TestMCPProxyEOFWithOutputBackpressure(t *testing.T) {
	listener, path := mcpProxySocket(t)
	f := newMCPProxyFixture(t, path)
	conn := acceptMCPProxy(t, listener)
	exchangeMCPProxy(t, f, conn, "before-backpressure")
	written := make(chan error, 1)
	go func() {
		_, err := conn.Write(bytes.Repeat([]byte("x"), 4<<20))
		written <- err
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-written:
		t.Fatalf("fixture did not exercise output backpressure: %v", err)
	default:
	}
	assertMCPProxyEOFExit(t, f)
}

func TestMCPProxyParentHelper(t *testing.T) {
	if os.Getenv("AGENTDECK_MCP_PARENT_HELPER") != "1" {
		return
	}
	cmd := exec.Command(os.Getenv("AGENTDECK_MCP_BINARY"), "mcp-proxy", os.Getenv("AGENTDECK_MCP_SOCKET"))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("AGENTDECK_MCP_PID_FILE"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		os.Exit(2)
	}
	for {
		if _, err := os.Stat(os.Getenv("AGENTDECK_MCP_EXIT_FILE")); err == nil {
			// Deliberately leave the child with inherited, still-open stdio.
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMCPProxyParentExitWithOpenStdin(t *testing.T) {
	listener, path := mcpProxySocket(t)
	dir := t.TempDir()
	pidFile, exitFile := filepath.Join(dir, "pid"), filepath.Join(dir, "exit")
	in, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out, reader, err := os.Pipe()
	if err != nil {
		_ = in.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = in.Close()
		_ = writer.Close()
		_ = out.Close()
		_ = reader.Close()
	})
	parent := exec.Command(os.Args[0], "-test.run=^TestMCPProxyParentHelper$")
	parent.Env = agentDeckTestEnv(dir, []string{
		"AGENT_DECK_TASK6_HELPER_PROCESS=1", "AGENTDECK_MCP_PARENT_HELPER=1",
		"AGENTDECK_MCP_BINARY=" + channelsCLIBinary(t), "AGENTDECK_MCP_SOCKET=" + path,
		"AGENTDECK_MCP_PID_FILE=" + pidFile, "AGENTDECK_MCP_EXIT_FILE=" + exitFile,
	})
	parent.Stdin, parent.Stdout, parent.Stderr = in, reader, os.Stderr
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	_ = in.Close()
	_ = reader.Close()
	done := make(chan error, 1)
	go func() { done <- parent.Wait() }()
	var proxy *os.Process
	t.Cleanup(func() {
		_ = writer.Close()
		_ = out.Close()
		if proxy == nil {
			if data, err := os.ReadFile(pidFile); err == nil {
				if pid, err := strconv.Atoi(string(data)); err == nil {
					proxy, _ = os.FindProcess(pid)
				}
			}
		}
		if proxy != nil {
			_ = proxy.Kill()
		}
		_ = parent.Process.Kill()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("parent fixture cleanup did not finish")
		}
	})
	conn := acceptMCPProxy(t, listener)
	f := &mcpProxyFixture{input: writer, output: bufio.NewReader(out)}
	exchangeMCPProxy(t, f, conn, "parent-alive")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	proxy, err = os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exitFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		done <- err
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("parent fixture did not exit")
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := conn.Read(b[:]); err != io.EOF {
		t.Fatalf("proxy kept backend open after parent exit with stdin open: %v", err)
	}
	if err := out.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.output.ReadByte(); err != io.EOF {
		t.Fatalf("proxy kept stdout open after parent exit: %v", err)
	}
}
