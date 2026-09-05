package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestNativeSSHAttachLifecycle(t *testing.T) {
	for _, tool := range []string{"ssh", "tmux", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			nativeSSHMissingTool(t, tool)
		}
	}
	bin := channelsCLIBinary(t)
	controller, remote, shim := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	proxy := startNativeSSH(t, remote, shim)
	pidFile := filepath.Join(controller, "ssh-client.pid")
	shimPath := filepath.Join(shim, "ssh")
	shimBody, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatal(err)
	}
	write(shimPath, strings.Replace(string(shimBody), "#!/bin/sh\n", "#!/bin/sh\nprintf '%s' \"$$\" > \"$NATIVE_SSH_PID_FILE\"\n", 1))
	write(filepath.Join(controller, ".config", "agent-deck", "config.toml"), fmt.Sprintf("[remotes.lab]\nhost = 'test-host'\nagent_deck_path = '%s'\n", bin))
	socket := fmt.Sprintf("native-%d", time.Now().UnixNano())
	write(filepath.Join(remote, ".config", "agent-deck", "config.toml"), "[tmux]\nsocket_name = '"+socket+"'\n")
	receiver := filepath.Join(remote, "receiver.py")
	write(receiver, `import os, tty, hashlib, uuid, sys
# Raw receiver acknowledges bytes without canonical line limits. This is transport
# evidence; rendered cell widths and physical-terminal behavior need a renderer.
tty.setraw(0)
generation = str(uuid.uuid4())
banner = "\033[32mREADY café 世界 e\u0301\033[0m APP:" + str(os.getpid()) + ":" + generation
sys.stdout.write("\033[2J\033[H\033[?2004h" + banner + "\r\n\033[31;44mA界e\u0301Z\033[0m")
sys.stdout.flush()
buf = bytearray()
escape = bytearray()
pasting = False
count = 0
while True:
    data = os.read(0, 8192)
    if not data: break
    for byte in data:
        if escape or byte == 27:
            escape.append(byte)
            if bytes(escape) in (b"\x1b[200~", b"\x1b[201~"):
                pasting = bytes(escape) == b"\x1b[200~"
                escape.clear()
                continue
            if any(marker.startswith(bytes(escape)) for marker in (b"\x1b[200~", b"\x1b[201~")):
                continue
            buf.extend(escape)
            escape.clear()
            continue
        if byte in (10, 13) and not pasting:
            if buf.startswith(b"@identity "):
                nonce = bytes(buf[len(b"@identity "):]).decode("ascii")
                print("\r\nID:" + nonce + " APP:" + str(os.getpid()) + ":" + generation, flush=True)
                buf.clear()
                continue
            count += 1
            print("\r\nRX:" + hashlib.sha256(buf).hexdigest() + ":" + str(len(buf)) + ":COUNT:" + str(count), flush=True)
            buf.clear()
        else:
            buf.append(byte)
`)
	localTERM := "xterm-256color"
	envFor := func(home string) []string {
		var env []string
		for _, kv := range os.Environ() {
			key := strings.SplitN(kv, "=", 2)[0]
			if key == "HOME" || key == "PATH" || key == "TERM" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "AGENTDECK_") || strings.HasPrefix(key, "TMUX") {
				continue
			}
			env = append(env, kv)
		}
		return append(env, "HOME="+home, "PATH="+shim+":"+os.Getenv("PATH"), "TERM="+localTERM, "NATIVE_SSH_PID_FILE="+pidFile)
	}
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = envFor(remote)
		cmd.Dir = remote
		out, err := cmd.CombinedOutput()
		t.Logf("CLI %v: exit=%v output=%s", args, err, out)
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, out)
		}
		return string(out)
	}
	run("add", remote, "--title", "native", "--cmd", "shell", "--wrapper", "python3 -u "+receiver, "--json")
	run("session", "start", "native", "--json")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, bin, "session", "stop", "native")
		command.Env = envFor(remote)
		output, err := command.CombinedOutput()
		t.Logf("session cleanup: %v %s", err, output)
		if ctx.Err() != nil {
			t.Error("session cleanup timed out")
		}
	})
	var tmuxName string
	// The name is stable and avoids relying on a registry title-to-tmux mapping.
	probe := func(format string) string {
		t.Helper()
		args := []string{"-L", socket, "display-message", "-p"}
		if tmuxName != "" {
			args = append(args, "-t", tmuxName)
		}
		args = append(args, format)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "tmux", args...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + remote}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("tmux probe: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	tmuxName = probe("#{session_name}")
	panePID := probe("#{pane_pid}")
	identity := probe("#{pid}:#{session_id}:#{pane_id}:#{pane_pid}")
	changed := make(chan struct{}, 1)
	waitFor := func(what string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if ready() {
				return
			}
			select {
			case <-changed:
			case <-time.After(10 * time.Millisecond):
			}
		}
		t.Fatalf("timed out: %s", what)
	}
	receipt := func(message string) string {
		return fmt.Sprintf("RX:%x:%d", sha256.Sum256([]byte(message)), len(message))
	}
	var writeMu sync.Mutex
	writeInput := func(terminal *os.File, input string) {
		t.Helper()
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := io.WriteString(terminal, input); err != nil {
			t.Fatal(err)
		}
	}
	directSSH := false
	multiplex := false
	lastFrame := "READY café 世界"
	appPattern := regexp.MustCompile(`APP:[0-9]+:[a-f0-9-]{36}`)
	var appIdentity string
	attachNumber := 0
	attach := func() (*os.File, *exec.Cmd, func() string, <-chan error) {
		t.Helper()
		cmd := exec.Command(bin, "remote", "attach", "lab", "native")
		if directSSH {
			quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
			cmd = exec.Command(shimPath, "-tt", "test-host", "TERM=xterm-256color "+quote(bin)+" session attach native")
		}
		cmd.Env = append(envFor(controller), fmt.Sprintf("NATIVE_SSH_MULTIPLEX=%t", multiplex))
		terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 40})
		if err != nil {
			t.Fatal(err)
		}
		attachNumber++
		thisAttach := attachNumber
		var mu sync.Mutex
		var output bytes.Buffer
		readerDone := make(chan struct{})
		go func() {
			defer close(readerDone)
			buf := make([]byte, 8192)
			var pending []byte
			for {
				n, err := terminal.Read(buf)
				if n > 0 {
					pending = append(pending, buf[:n]...)
					for _, query := range []struct{ request, response string }{
						{"\x1b]11;?\x1b\\", "\x1b]11;rgb:0000/0000/0000\x1b\\"},
						{"\x1b]11;?\a", "\x1b]11;rgb:0000/0000/0000\x1b\\"},
						{"\x1b[6n", "\x1b[1;1R"},
					} {
						for bytes.Contains(pending, []byte(query.request)) {
							writeMu.Lock()
							_, _ = io.WriteString(terminal, query.response)
							writeMu.Unlock()
							pending = bytes.Replace(pending, []byte(query.request), nil, 1)
						}
					}
					if len(pending) > 64 {
						pending = pending[len(pending)-64:]
					}
					mu.Lock()
					output.Write(buf[:n])
					mu.Unlock()
					select {
					case changed <- struct{}{}:
					default:
					}
				}
				if err != nil {
					return
				}
			}
		}()
		snapshot := func() string { mu.Lock(); defer mu.Unlock(); return output.String() }
		done := make(chan error, 1)
		processDone := make(chan struct{})
		go func() { done <- cmd.Wait(); close(processDone) }()
		t.Cleanup(func() {
			_ = terminal.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			// Dedicated completion channels are safe after the test has already
			// consumed the process error from done.
			select {
			case <-processDone:
			case <-time.After(3 * time.Second):
				t.Error("attach process cleanup timed out")
			}
			select {
			case <-readerDone:
			case <-time.After(3 * time.Second):
				t.Error("attach output cleanup timed out")
			}

			if dir := os.Getenv("NATIVE_SSH_RECEIPT_DIR"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Error(err)
				}
				file, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("attach-%02d.txt", thisAttach)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					t.Error(err)
				} else {
					_, err = file.WriteString(snapshot())
					if err != nil {
						t.Error(err)
					}
					file.Close()
				}
			}
			if t.Failed() {
				t.Logf("attach output: %q", snapshot())
			}
		})
		waitFor("remote first frame", func() bool { return strings.Contains(snapshot(), lastFrame) })
		if thisAttach == 1 {
			waitFor("initial application identity", func() bool { return appPattern.MatchString(snapshot()) })
			appIdentity = appPattern.FindString(snapshot())
			if !strings.Contains(snapshot(), "[32m") {
				t.Error("initial remote color missing")
			}
		} else {
			nonce := fmt.Sprintf("attach-%02d", thisAttach)
			expected := "ID:" + nonce + " " + appIdentity
			writeInput(terminal, "@identity "+nonce+"\r")
			waitFor("fresh matching application identity", func() bool { return strings.Contains(snapshot(), expected) })
			t.Logf("reconnect identity: %s", expected)
		}
		return terminal, cmd, snapshot, done
	}
	terminal, client, snapshot, done := attach()
	waitFor("initial attach dimensions", func() bool { return probe("#{window_width}x#{window_height}") == "120x39" })
	captureGrid := func(escaped bool) string {
		t.Helper()
		args := []string{"-L", socket, "capture-pane", "-p", "-t", tmuxName}
		if escaped {
			args = append(args, "-e")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "tmux", args...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + remote}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("grid capture: %v: %s", err, output)
		}
		return string(output)
	}
	waitFor("initial Unicode grid", func() bool { return strings.Contains(captureGrid(false), "A界e\u0301Z") })
	if cursor := probe("#{cursor_x}:#{cursor_y}"); cursor != "5:1" {
		t.Errorf("wide/combining cell cursor=%s want5:1", cursor)
	}
	grid := captureGrid(true)
	foreground := regexp.MustCompile(`\x1b\[[0-9;]*31[;m]`)
	background := regexp.MustCompile(`\x1b\[[0-9;]*44[;m]`)
	if !foreground.MatchString(grid) || !background.MatchString(grid) {
		t.Errorf("tmux color attributes missing: %q", grid)
	}
	t.Logf("initial tmux rendered grid: %q; server cells only, physical client rendering untested", grid)
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 150, Rows: 45}); err != nil {
		t.Fatal(err)
	}
	client.Process.Signal(syscall.SIGWINCH)
	waitFor("remote resize", func() bool { return probe("#{window_width}x#{window_height}") == "150x44" })
	for _, size := range []pty.Winsize{{Cols: 100, Rows: 35}, {Cols: 140, Rows: 48}, {Cols: 150, Rows: 45}} {
		if err := pty.Setsize(terminal, &size); err != nil {
			t.Fatal(err)
		}
		if err := client.Process.Signal(syscall.SIGWINCH); err != nil {
			t.Fatal(err)
		}
		expected := fmt.Sprintf("%dx%d", size.Cols, size.Rows-1)
		waitFor("repeated remote resize", func() bool { return probe("#{window_width}x#{window_height}") == expected })
	}
	// A terminal-generated OSC reply must not become application input.
	writeInput(terminal, "\x1b]11;rgb:abcd/0000/ffff\a")
	writeInput(terminal, "ordered-input\r")
	waitFor("ordered input receipt", func() bool { return strings.Contains(snapshot(), receipt("ordered-input")) })
	if !strings.Contains(snapshot(), receipt("ordered-input")) {
		t.Errorf("terminal reply reached application: %q", snapshot())
	}

	// Fragment writes across UTF-8 and payload sizes around the pump buffer.
	// The OS may coalesce writes; this does not prove individual read boundaries.
	var latencies []time.Duration
	for index, size := range []int{1, 255, 256, 257, 511, 512, 513} {
		frame := fmt.Sprintf("frame-%02d-世界-", index) + strings.Repeat("x", size)
		started := time.Now()
		for position := 0; position < len(frame); {
			end := position + 7
			if end > len(frame) {
				end = len(frame)
			}
			writeInput(terminal, frame[position:end])
			position = end
		}
		writeInput(terminal, "\r")
		waitFor("fragmented frame", func() bool { return strings.Contains(snapshot(), receipt(frame)+fmt.Sprintf(":COUNT:%d", index+2)) })
		latencies = append(latencies, time.Since(started))
	}
	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	t.Logf("fragmented input latency samples=%v median=%v max=%v; characterization only, no direct-SSH baseline or acceptance budget", latencies, sorted[len(sorted)/2], sorted[len(sorted)-1])
	paste := strings.Repeat("世a\n", 819) + "x" // 819*5+1 = 4096 UTF-8 bytes.
	writeInput(terminal, "\x1b[200~"+paste+"\x1b[201~")
	if strings.Contains(snapshot(), receipt(paste)) {
		t.Fatal("paste submitted without Enter")
	}
	writeInput(terminal, "\r")
	waitFor("bracketed 4KB paste", func() bool { return strings.Contains(snapshot(), receipt(paste)+":COUNT:9") })
	message := strings.Repeat("abcd", 1024)
	writeInput(terminal, message+"\r")
	waitFor("4KB interactive receipt", func() bool { return strings.Contains(snapshot(), receipt(message)+":COUNT:10") })
	// The CLI send path must submit its own 4KB message even with --no-wait.
	sendMessage := strings.Repeat("wxyz", 1024)
	run("session", "send", "native", sendMessage, "--no-wait", "--json")
	waitFor("4KB no-wait send receipt", func() bool { return strings.Contains(snapshot(), receipt(sendMessage)+":COUNT:11") })
	barrier := "after-cli-send-barrier"
	writeInput(terminal, barrier+"\r")
	waitFor("exactly-once CLI send barrier", func() bool { return strings.Contains(snapshot(), receipt(barrier)+":COUNT:12") })
	// Repaints may repeat the same receipt bytes; a different receiver count
	// for this payload is an actual duplicate submission.
	cliReceipts := regexp.MustCompile(regexp.QuoteMeta(receipt(sendMessage)) + `:COUNT:([0-9]+)`)
	for _, match := range cliReceipts.FindAllStringSubmatch(snapshot(), -1) {
		if match[1] != "11" {
			t.Errorf("CLI payload submitted again at receiver count%s", match[1])
		}
	}
	lastFrame = receipt(barrier)
	writeInput(terminal, "\x11")
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Ctrl+Q detach: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Ctrl+Q did not detach")
	}
	if got := probe("#{pane_pid}"); got != panePID {
		t.Fatalf("detach restarted application: %s != %s", got, panePID)
	}
	terminal.Close()
	terminal, _, snapshot, done = attach()
	waitFor("reconnect transcript", func() bool { return strings.Contains(snapshot(), receipt(sendMessage)) })
	writeInput(terminal, "alive-after-detach\r")
	waitFor("live after detach", func() bool { return strings.Contains(snapshot(), receipt("alive-after-detach")) })
	lastFrame = receipt("alive-after-detach")
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	sshClient, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := sshClient.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("SSH loss incorrectly reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SSH client loss hung attach")
	}
	if got := probe("#{pane_pid}"); got != panePID {
		t.Fatalf("SSH loss restarted application: %s != %s", got, panePID)
	}
	terminal.Close()
	localTERM = "xterm-ghostty"
	terminal, _, snapshot, done = attach()
	waitFor("SSH reconnect transcript", func() bool { return strings.Contains(snapshot(), receipt(sendMessage)) })
	writeInput(terminal, "alive-after-ssh-loss\r")
	waitFor("live receiver after SSH reconnect", func() bool { return strings.Contains(snapshot(), receipt("alive-after-ssh-loss")) })
	lastFrame = receipt("alive-after-ssh-loss")
	if output := run("session", "show", "native", "--json"); !strings.Contains(output, "\"status\": \"idle\"") {
		t.Errorf("unexpected status after reconnect: %s", output)
	}
	if got := probe("#{pid}:#{session_id}:#{pane_id}:#{pane_pid}"); got != identity {
		t.Fatalf("identity changed: %s != %s", got, identity)
	}

	proxy.drop()
	select {
	case err := <-done:
		if err == nil {
			t.Error("abrupt network close reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("abrupt network close hung attach")
	}
	terminal.Close()
	terminal, _, snapshot, done = attach()
	writeInput(terminal, "alive-after-network-close\r")
	waitFor("live after network close", func() bool { return strings.Contains(snapshot(), receipt("alive-after-network-close")) })
	lastFrame = receipt("alive-after-network-close")
	proxy.pause(true)
	writeInput(terminal, "held-during-blackhole\r")
	// Hold the transport long enough to establish a real interrupted interval.
	time.Sleep(250 * time.Millisecond)
	if strings.Contains(snapshot(), receipt("held-during-blackhole")) {
		t.Fatal("blackhole forwarded receipt")
	}
	proxy.pause(false)
	waitFor("restored network", func() bool { return strings.Contains(snapshot(), receipt("held-during-blackhole")) })
	lastFrame = receipt("held-during-blackhole")
	proxy.pause(true)
	writeInput(terminal, "\x11")
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("blackhole manual detach: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("blackhole manual detach hung")
	}
	terminal.Close()
	proxy.pause(false)
	terminal, _, snapshot, done = attach()
	writeInput(terminal, "alive-after-blackhole\r")
	waitFor("live after blackhole reconnect", func() bool { return strings.Contains(snapshot(), receipt("alive-after-blackhole")) })
	lastFrame = receipt("alive-after-blackhole")
	if got := probe("#{pid}:#{session_id}:#{pane_id}:#{pane_pid}"); got != identity {
		t.Fatalf("network fault changed identity: %s != %s", got, identity)
	}

	writeInput(terminal, "\x11")
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("final detach hung")
	}
	terminal.Close()
	lastFrame = receipt("alive-after-blackhole")
	// Matched measurements use the same OpenSSH configuration, proxy, remote
	// tmux pane and receiver. A 50ms p95 delta guards a new polling delay;
	// absolute bounds remain deliberately loose for a shared test runner.
	measure := func(label string) []time.Duration {
		t.Helper()
		terminal, _, snapshot, done = attach()
		samples := make([]time.Duration, 0, 30)
		for index := 0; index < 30; index++ {
			frame := fmt.Sprintf("latency-%s-%02d", label, index)
			start := time.Now()
			writeInput(terminal, frame+"\r")
			waitFor("latency receipt", func() bool { return strings.Contains(snapshot(), receipt(frame)) })
			samples = append(samples, time.Since(start))
			lastFrame = receipt(frame)
		}
		writeInput(terminal, "\x11")
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s latency detach: %v", label, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("latency detach hung")
		}
		terminal.Close()
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		t.Logf("latency %s sorted=%v median=%v p95=%v max=%v", label, samples, samples[15], samples[28], samples[29])
		return samples
	}
	directSSH = true
	direct := measure("direct")
	directSSH = false
	deck := measure("deck")
	if deck[28] > direct[28]+50*time.Millisecond {
		t.Errorf("Agent Deck p95=%v exceeds directSSH=%v plus50ms", deck[28], direct[28])
	}
	if deck[29] > time.Second {
		t.Errorf("Agent Deck max latency=%v exceeds1s", deck[29])
	}
	// Exercise the production ControlMaster options after the isolated
	// no-master fault tests. The runner owns the whole socket namespace.
	multiplex = true
	control := func(operation string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, shimPath, "-O", operation, "-o", "ControlPath=/tmp/agent-deck-ssh/%r@%h:%p", "test-host")
		command.Env = append(envFor(controller), "NATIVE_SSH_MULTIPLEX=true")
		out, err := command.CombinedOutput()
		return string(out), err
	}
	t.Cleanup(func() { output, err := control("exit"); t.Logf("fixture master exit: %v %s", err, output) })
	terminal, _, snapshot, done = attach()
	master, err := control("check")
	if err != nil {
		t.Fatalf("production master not active: %v %s", err, master)
	}
	t.Logf("production master: %s", master)
	writeInput(terminal, "multiplex-first\r")
	waitFor("multiplex input", func() bool { return strings.Contains(snapshot(), receipt("multiplex-first")) })
	lastFrame = receipt("multiplex-first")
	writeInput(terminal, "\x1b[113;5u")
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("CSI-u detach: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CSI-u detach hung")
	}
	terminal.Close()
	terminal, _, snapshot, done = attach()
	reconnectMaster, err := control("check")
	if err != nil || reconnectMaster != master {
		t.Errorf("master changed on reconnect: %v %q want%q", err, reconnectMaster, master)
	}
	writeInput(terminal, "multiplex-reconnect\r")
	waitFor("multiplex reconnect", func() bool { return strings.Contains(snapshot(), receipt("multiplex-reconnect")) })
	lastFrame = receipt("multiplex-reconnect")
	writeInput(terminal, "\x1b[27;5;113~")
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("modifyOtherKeys detach: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("modifyOtherKeys detach hung")
	}
	terminal.Close()
	terminal, _, snapshot, done = attach()
	// A delayed split exercises escape-sequence handling across transport
	// writes. The kernel may coalesce reads; no particular read boundary is assumed.
	writeInput(terminal, "\x1b[113;")
	time.Sleep(100 * time.Millisecond)
	writeInput(terminal, "5u")
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("fragmented CSI-u detach: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("delayed-split CSI-u CtrlQ did not detach within2s")
		writeInput(terminal, "\x11")
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("fallback detach hung")
		}
	}
	terminal.Close()
	if got := probe("#{pid}:#{session_id}:#{pane_id}:#{pane_pid}"); got != identity {
		t.Errorf("final session identity changed: %s != %s", got, identity)
	}

}
