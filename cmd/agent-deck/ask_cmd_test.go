package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/ask"
)

// askHelperVaultEnv makes this test binary act as the agent-deck CLI with
// the fake vault at its value swapped in (ask.SetVaultForTest, which only a
// test binary can call). The release binary has no such switch: only this
// _test.go file reads the variable.
const askHelperVaultEnv = "AGENTDECK_ASK_TEST_FAKE_VAULT"

func askHelperMain() {
	vault := os.Getenv(askHelperVaultEnv)
	if vault == "" {
		return
	}
	restore := ask.SetVaultForTest(vault)
	defer restore()
	os.Args = append([]string{"agent-deck"}, os.Args[1:]...)
	main()
	os.Exit(0)
}

// askFakeVault mirrors internal/ask's test vault: one file per name under
// store, the argv and env of every run, deck-vault's stderr line.
const askFakeVault = `#!/bin/sh
store='%s'
printf '%%s\n' "$*" >> "$store/.argv"
env >> "$store/.env"
echo '--' >> "$store/.env"
n=$(printf '%%s' "$2" | tr '/' '_')
case "$1" in
exists)
	if [ -f "$store/$n" ]; then echo yes; exit 0; fi
	echo no; exit 3 ;;
set)
	existed=no
	[ -f "$store/$n" ] && existed=yes
	cat > "$store/$n"
	printf '%%s' "$AGENTDECK_SESSION_ID" > "$store/$n.session"
	echo "stored $2 (replaced=$existed)" >&2
	exit 0 ;;
esac
exit 2
`

type askCLI struct {
	t       *testing.T
	home    string
	store   string
	vault   string
	bin     string // this test binary acting as the CLI
	profile string
	extra   []string // more env for every run (AGENTDECK_DEBUG=1)
}

func newAskCLI(t *testing.T) *askCLI {
	t.Helper()
	home := t.TempDir()
	store := t.TempDir()
	vault := filepath.Join(t.TempDir(), "deck-secret")
	if err := os.WriteFile(vault, []byte(fmt.Sprintf(askFakeVault, store)), 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &askCLI{t: t, home: home, store: store, vault: vault, bin: self, profile: "ashesh"}
}

func (c *askCLI) env(extra ...string) []string {
	return agentDeckTestEnv(c.home, append(append([]string{askHelperVaultEnv + "=" + c.vault}, c.extra...), extra...))
}

func (c *askCLI) args(args ...string) []string {
	return append([]string{"-p", c.profile}, args...)
}

// askRunDeadline bounds every foreground CLI run of these tests.
const askRunDeadline = 60 * time.Second

// run runs the CLI (fake vault) to completion, killed at askRunDeadline.
func (c *askCLI) run(stdin string, extraEnv []string, args ...string) (string, string, int) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), askRunDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, c.args(args...)...)
	cmd.Env = c.env(extraEnv...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			c.t.Fatalf("run %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	if ctx.Err() != nil {
		c.t.Fatalf("run %v: still running after %v", args, askRunDeadline)
	}
	return out.String(), errOut.String(), code
}

// lockedBuffer is a bytes.Buffer safe to read while a child writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type askBackground struct {
	cmd    *exec.Cmd
	stdout *lockedBuffer
	stderr *lockedBuffer
	done   chan int
}

// start runs the CLI in the background (the asker).
func (c *askCLI) start(args ...string) *askBackground {
	c.t.Helper()
	cmd := exec.Command(c.bin, c.args(args...)...)
	cmd.Env = c.env()
	bg := &askBackground{cmd: cmd, stdout: &lockedBuffer{}, stderr: &lockedBuffer{}, done: make(chan int, 1)}
	cmd.Stdout, cmd.Stderr = bg.stdout, bg.stderr
	if err := cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	go func() {
		err := cmd.Wait()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		bg.done <- code
	}()
	c.t.Cleanup(func() {
		select {
		case <-bg.done:
		default:
			_ = cmd.Process.Kill() // our own child, by its exact pid
			<-bg.done
		}
	})
	return bg
}

func (bg *askBackground) wait(t *testing.T, within time.Duration) int {
	t.Helper()
	select {
	case code := <-bg.done:
		bg.done <- code
		return code
	case <-time.After(within):
		t.Fatalf("asker still running after %v\nstdout: %s\nstderr: %s", within, bg.stdout, bg.stderr)
	}
	return -1
}

func (c *askCLI) addSession(title string) string {
	c.t.Helper()
	out, errOut, code := c.run("", nil, "add", c.t.TempDir(), "--title", title, "-c", "bash", "--no-parent", "--json")
	if code != 0 {
		c.t.Fatalf("add: %d %s %s", code, out, errOut)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil || added.ID == "" {
		c.t.Fatalf("add result: %s %v", out, err)
	}
	return added.ID
}

type askListed struct {
	RequestID  string   `json:"request_id"`
	Kind       string   `json:"kind"`
	SessionID  string   `json:"session_id"`
	Name       string   `json:"name"`
	Replace    bool     `json:"replace"`
	AskEventID string   `json:"ask_event_id"`
	State      string   `json:"state"`
	SeenBy     []string `json:"seen_by"`
}

// waitRequest waits for the session's pending, announced request.
func (c *askCLI) waitRequest(sessionID string) askListed {
	c.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out, _, code := c.run("", nil, "ask", "list", "--json")
		var list []askListed
		if code == 0 && json.Unmarshal([]byte(out), &list) == nil {
			for _, r := range list {
				if r.SessionID == sessionID && r.AskEventID != "" {
					return r
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.t.Fatalf("no pending request for session %s", sessionID)
	return askListed{}
}

// appSeen does what the Mac app does when the dialog is on screen: publish
// macapp.ask.seen with a fresh nonce over `events publish` (ungated), then
// confirm through `ask show --json` that the host recorded it.
func (c *askCLI) appSeen(r askListed) string {
	c.t.Helper()
	nonce := randomHex(c.t, 8)
	data, _ := json.Marshal(map[string]string{"request_id": r.RequestID, "ask_event_id": r.AskEventID, "nonce": nonce, "by": "app"})
	if out, errOut, code := c.run(string(data), nil, "events", "publish", "--kind", "macapp.ask.seen", "--session", r.SessionID, "--data-file", "-"); code != 0 {
		c.t.Fatalf("events publish seen: %d %s %s", code, out, errOut)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out, _, _ := c.run("", nil, "ask", "show", r.RequestID, "--json")
		var shown askListed
		if json.Unmarshal([]byte(out), &shown) == nil && len(shown.SeenBy) > 0 && shown.SeenBy[0] == "app" {
			return nonce
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.t.Fatalf("the asker never recorded the app's seen frame")
	return ""
}

func deliverInput(r askListed, nonce string, value []byte) string {
	h, _ := json.Marshal(map[string]any{"request_id": r.RequestID, "kind": r.Kind, "name": r.Name, "replace": r.Replace, "nonce": nonce, "answered_by": "app"})
	return string(h) + "\n" + string(value)
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func (c *askCLI) vaultValue(name string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(c.store, strings.ReplaceAll(name, "/", "_")))
	return string(b), err == nil
}

// The owner's acceptance case: `agent-deck ask secret
// conductor/GH_TOKEN_DECKBOX --reason "GitHub key with workflow permission"`
// end to end through the real CLI, the app's seen + deliver route and a
// fake vault.
func TestAskSecretAcceptanceEndToEnd(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Conductor")
	asker := c.start("ask", "secret", "conductor/GH_TOKEN_DECKBOX", "--reason", "GitHub key with workflow permission", "--session", "Conductor")
	r := c.waitRequest(sid)
	if r.Kind != "secret" || r.Name != "conductor/GH_TOKEN_DECKBOX" || r.Replace {
		t.Fatalf("listed request %+v", r)
	}
	nonce := c.appSeen(r)
	value := "ghp_acceptance_" + randomHex(t, 12)
	out, errOut, code := c.run(deliverInput(r, nonce, []byte(value+"\n")), nil, "ask", "deliver", r.RequestID)
	if code != 0 || strings.TrimSpace(out) != `{"status":"stored","replaced":"no"}` {
		t.Fatalf("deliver: exit %d stdout %q stderr %q", code, out, errOut)
	}
	if code := asker.wait(t, 20*time.Second); code != 0 {
		t.Fatalf("asker exit %d, stderr %q", code, asker.stderr)
	}
	if got := asker.stdout.String(); got != "stored conductor/GH_TOKEN_DECKBOX (replaced=no, by app)\n" {
		t.Fatalf("asker stdout %q", got)
	}
	if v, ok := c.vaultValue("conductor/GH_TOKEN_DECKBOX"); !ok || v != value {
		t.Fatal("the fake vault did not get the value")
	}
	if sess, _ := os.ReadFile(filepath.Join(c.store, "conductor_GH_TOKEN_DECKBOX.session")); string(sess) != sid {
		t.Fatalf("vault saw AGENTDECK_SESSION_ID=%q, want %q", sess, sid)
	}
	// A second deliver (replay) is refused with a fixed message.
	out, errOut, code = c.run(deliverInput(r, nonce, []byte("attacker")), nil, "ask", "deliver", r.RequestID)
	if code != 2 || !strings.Contains(errOut, "not pending") || strings.Contains(out+errOut, "attacker") {
		t.Fatalf("replayed deliver: exit %d stdout %q stderr %q", code, out, errOut)
	}
}

// Acceptance line 1: the value reaches the fake vault and nothing else. A
// random sentinel goes through `ask deliver`, every process runs with
// AGENTDECK_DEBUG=1; then every file under the temp HOME (events log
// segments, ask dir, the debug log, state.db), both processes'
// stdout/stderr and their /proc cmdline/environ sampled during the run must
// not contain it, and a tmux pane running an asker is captured and grepped
// (tmux is required in the test container). The route of the asker's output
// through a real session's hooks, transcript, inbox and conductor is
// TestAskSecretNeverLeaksThroughAgentSession.
func TestAskSecretNeverLeaks(t *testing.T) {
	parallelSubprocessTest(t)
	requireAskTools(t, "tmux")
	c := newAskCLI(t)
	c.extra = []string{"AGENTDECK_DEBUG=1"}
	sid := c.addSession("Conductor")
	sentinel := []byte("SENTINEL-" + randomHex(t, 16))
	hits := func(b []byte) bool { return bytes.Contains(b, sentinel) }

	var procMu sync.Mutex
	var procSeen [][]byte
	sample := func(pid int) {
		for _, f := range []string{"cmdline", "environ"} {
			if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/%s", pid, f)); err == nil {
				procMu.Lock()
				procSeen = append(procSeen, b)
				procMu.Unlock()
			}
		}
	}

	asker := c.start("ask", "secret", "conductor/LEAK_PROBE", "--reason", "leak probe", "--session", sid)
	stopSampling := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		for {
			select {
			case <-stopSampling:
				return
			default:
			}
			sample(asker.cmd.Process.Pid)
			time.Sleep(20 * time.Millisecond)
		}
	}()
	r := c.waitRequest(sid)
	nonce := c.appSeen(r)

	deliver := exec.Command(c.bin, c.args("ask", "deliver", r.RequestID)...)
	deliver.Env = c.env()
	deliver.Stdin = strings.NewReader(deliverInput(r, nonce, append(append([]byte{}, sentinel...), '\n')))
	var dOut, dErr bytes.Buffer
	deliver.Stdout, deliver.Stderr = &dOut, &dErr
	if err := deliver.Start(); err != nil {
		t.Fatal(err)
	}
	sample(deliver.Process.Pid)
	if err := deliver.Wait(); err != nil {
		t.Fatalf("deliver: %v %s", err, dErr.String())
	}
	if code := asker.wait(t, 20*time.Second); code != 0 {
		t.Fatalf("asker exit %d: %s", code, asker.stderr)
	}
	close(stopSampling)
	sampler.Wait()

	if v, _ := c.vaultValue("conductor/LEAK_PROBE"); v != string(sentinel) {
		t.Fatal("the fake vault did not get the sentinel")
	}
	for name, b := range map[string]string{"asker stdout": asker.stdout.String(), "asker stderr": asker.stderr.String(), "deliver stdout": dOut.String(), "deliver stderr": dErr.String()} {
		if hits([]byte(b)) {
			t.Errorf("sentinel in %s", name)
		}
	}
	if len(procSeen) == 0 {
		t.Error("no /proc sample was taken")
	}
	for _, b := range procSeen {
		if hits(b) {
			t.Error("sentinel in a process's /proc cmdline or environ")
		}
	}

	paneLeakCheck(t, c, sid, sentinel)

	// Every file the run could have touched: the whole temp HOME, the vault
	// store (only the value file may hold it) and the vault's argv/env log.
	var walked, eventsWithAck int
	var debugLog []byte
	for _, root := range []string{c.home, c.store} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			walked++
			if d.Name() == "debug.log" {
				debugLog = append(debugLog, b...)
			}
			if bytes.Contains(b, []byte("macapp.ask.ack")) {
				eventsWithAck++
			}
			if hits(b) && path != filepath.Join(c.store, "conductor_LEAK_PROBE") && path != filepath.Join(c.store, "conductor_LEAK_PANE") {
				t.Errorf("sentinel found in %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if eventsWithAck == 0 || walked < 5 {
		t.Fatalf("walk covered %d files, %d with the ack frame: the events log was not reached", walked, eventsWithAck)
	}
	// AGENTDECK_DEBUG=1: the asker and deliver wrote their debug log, so
	// the walk above grepped a real one.
	if len(debugLog) == 0 || !bytes.Contains(debugLog, []byte(`"component":"ask"`)) || !bytes.Contains(debugLog, []byte(r.RequestID)) {
		t.Fatalf("AGENTDECK_DEBUG=1: no debug.log with the ask lines of request %s under the temp HOME (%d bytes)", r.RequestID, len(debugLog))
	}
}

// paneLeakCheck runs an asker in a tmux pane on a private socket (TestMain
// isolated TMUX_TMPDIR) and greps the pane capture after a deliver.
func paneLeakCheck(t *testing.T, c *askCLI, sid string, sentinel []byte) {
	t.Helper()
	sock := "ask-leak-" + randomHex(t, 4)
	tmuxCmd := func(args ...string) *exec.Cmd {
		cmd := exec.Command("tmux", append([]string{"-L", sock}, args...)...)
		cmd.Env = c.env()
		return cmd
	}
	var line []string
	for _, kv := range c.env() {
		line = append(line, shellQuote(kv))
	}
	script := "env -i " + strings.Join(line, " ") + " " + shellQuote(c.bin) + " -p " + c.profile +
		" ask secret conductor/LEAK_PANE --reason pane --session " + sid + "; echo ask-exit=$?; sleep 60"
	if out, err := tmuxCmd("new-session", "-d", "-s", "askleak", "-x", "200", "-y", "50", "sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session for the pane check: %v %s", err, out)
	}
	defer func() { _ = tmuxCmd("kill-session", "-t", "askleak").Run() }()
	r := c.waitRequestName(sid, "conductor/LEAK_PANE")
	nonce := c.appSeen(r)
	if out, errOut, code := c.run(deliverInput(r, nonce, sentinel), nil, "ask", "deliver", r.RequestID); code != 0 {
		t.Fatalf("pane deliver: %d %s %s", code, out, errOut)
	}
	var pane []byte
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		pane, _ = tmuxCmd("capture-pane", "-p", "-J", "-S", "-", "-t", "askleak").Output()
		if bytes.Contains(pane, []byte("ask-exit=")) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !bytes.Contains(pane, []byte("stored conductor/LEAK_PANE (replaced=no, by app)")) || !bytes.Contains(pane, []byte("ask-exit=0")) {
		t.Fatalf("pane:\n%s", pane)
	}
	if bytes.Contains(pane, sentinel) {
		t.Fatal("sentinel in the asker's tmux pane")
	}
	t.Logf("tmux pane capture checked (%d bytes, no sentinel)", len(pane))
}

func (c *askCLI) waitRequestName(sessionID, name string) askListed {
	c.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out, _, _ := c.run("", nil, "ask", "list", "--json")
		var list []askListed
		if json.Unmarshal([]byte(out), &list) == nil {
			for _, r := range list {
				if r.SessionID == sessionID && r.Name == name && r.AskEventID != "" {
					return r
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.t.Fatalf("no pending request %s", name)
	return askListed{}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func TestAskSecretRefusesExistingNameWithoutReplace(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Conductor")
	if err := os.WriteFile(filepath.Join(c.store, "conductor_GH_TOKEN"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := c.run("", nil, "ask", "secret", "conductor/GH_TOKEN", "--session", sid)
	if code != 2 || !strings.Contains(errOut, "--replace") || out != "" {
		t.Fatalf("existing name: exit %d stdout %q stderr %q", code, out, errOut)
	}
	asker := c.start("ask", "secret", "conductor/GH_TOKEN", "--replace", "--session", sid)
	r := c.waitRequest(sid)
	if !r.Replace {
		t.Fatal("record does not carry replace")
	}
	nonce := c.appSeen(r)
	if out, errOut, code := c.run(deliverInput(r, nonce, []byte("new")), nil, "ask", "deliver", r.RequestID); code != 0 || !strings.Contains(out, `"replaced":"yes"`) {
		t.Fatalf("deliver: %d %s %s", code, out, errOut)
	}
	if code := asker.wait(t, 20*time.Second); code != 0 {
		t.Fatalf("asker exit %d", code)
	}
	if !strings.Contains(asker.stderr.String(), "REPLACES the existing secret conductor/GH_TOKEN") {
		t.Fatalf("no replace warning: %q", asker.stderr)
	}
	if got := asker.stdout.String(); got != "stored conductor/GH_TOKEN (replaced=yes, by app)\n" {
		t.Fatalf("asker stdout %q", got)
	}
}

func TestAskDeliverHeaderMismatchRefusedCLI(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Conductor")
	asker := c.start("ask", "secret", "conductor/GH_TOKEN", "--session", sid, "--wait", "1m")
	r := c.waitRequest(sid)
	nonce := c.appSeen(r)
	value := "VALUE-" + randomHex(t, 8)
	bad := r
	bad.Name = "conductor/OTHER"
	out, errOut, code := c.run(deliverInput(bad, nonce, []byte(value)), nil, "ask", "deliver", r.RequestID)
	if code != 2 || !strings.Contains(errOut, "header does not match the request") || strings.Contains(out+errOut, value) {
		t.Fatalf("mismatched header: exit %d stdout %q stderr %q", code, out, errOut)
	}
	if _, ok := c.vaultValue("conductor/OTHER"); ok {
		t.Fatal("value went to a name the owner never saw")
	}
	if _, ok := c.vaultValue("conductor/GH_TOKEN"); ok {
		t.Fatal("value stored despite the mismatch")
	}
	if _, _, code := c.run("", nil, "ask", "cancel", r.RequestID, "--by", "app", "--nonce", nonce); code != 0 {
		t.Fatal("cancel")
	}
	asker.wait(t, 20*time.Second)
}

func TestAskDeliverStdinCapCLI(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	header := `{"request_id":"0123456789abcdef","kind":"secret","name":"conductor/X","replace":false,"nonce":"0123456789abcdef","answered_by":"app"}`
	big := strings.Repeat("v", ask.MaxSecretBytes+2)
	out, errOut, code := c.run(header+"\n"+big, nil, "ask", "deliver", "0123456789abcdef")
	if code != 2 || !strings.Contains(errOut, "over 64 KiB") || strings.Contains(out+errOut, "vvvv") {
		t.Fatalf("oversized value: exit %d stdout %q stderr %.200q", code, out, errOut)
	}
	out, errOut, code = c.run(strings.Repeat("h", 5000)+"\nvalue", nil, "ask", "deliver", "0123456789abcdef")
	if code != 2 || strings.Contains(out+errOut, "value") && !strings.Contains(errOut, "header") {
		t.Fatalf("oversized header: exit %d stdout %q stderr %q", code, out, errOut)
	}
	// The value never comes from argv.
	if _, errOut, code := c.run("", nil, "ask", "deliver", "0123456789abcdef", "secret-in-argv"); code != 2 || strings.Contains(errOut, "secret-in-argv") {
		t.Fatalf("value in argv: exit %d stderr %q", code, errOut)
	}
}

func TestAskCancelExit3(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Planner")
	asker := c.start("ask", "choice", "Which database?", "--option", "staging", "--option", "prod", "--session", sid)
	r := c.waitRequest(sid)
	nonce := c.appSeen(r)
	out, errOut, code := c.run("", nil, "ask", "cancel", r.RequestID, "--by", "app", "--nonce", nonce)
	if code != 0 || strings.TrimSpace(out) != `{"status":"cancelled"}` {
		t.Fatalf("cancel: %d %q %q", code, out, errOut)
	}
	if code := asker.wait(t, 20*time.Second); code != 3 {
		t.Fatalf("asker exit %d, want 3", code)
	}
	if asker.stdout.String() != "" || !strings.Contains(asker.stderr.String(), "cancelled by app") {
		t.Fatalf("asker stdout %q stderr %q", asker.stdout, asker.stderr)
	}
}

func TestAskChoiceAnsweredPrintsOnlyTheAnswer(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Planner")
	asker := c.start("ask", "choice", "Which database?", "--option", "staging", "--option", "prod", "--session", sid)
	r := c.waitRequest(sid)
	nonce := c.appSeen(r)
	if out, errOut, code := c.run(deliverInput(r, nonce, []byte("prod\n")), nil, "ask", "deliver", r.RequestID); code != 0 || strings.TrimSpace(out) != `{"status":"answered","replaced":"no"}` {
		t.Fatalf("deliver: %d %q %q", code, out, errOut)
	}
	if code := asker.wait(t, 20*time.Second); code != 0 || asker.stdout.String() != "prod\n" {
		t.Fatalf("asker exit %d stdout %q", code, asker.stdout)
	}
	// The answer is cleared from the record once printed.
	found := false
	_ = filepath.WalkDir(c.home, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == r.RequestID+".json" {
			found = true
			if b, _ := os.ReadFile(path); bytes.Contains(b, []byte(`"answer"`)) {
				t.Errorf("answer kept in the record: %s", b)
			}
		}
		return nil
	})
	if !found {
		t.Fatal("record not found")
	}
}

func TestAskTextRefusesCredentialAnswerCLI(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Planner")
	asker := c.start("ask", "text", "Paste your GitHub token", "--session", sid)
	r := c.waitRequest(sid)
	nonce := c.appSeen(r)
	token := "ghp_" + randomHex(t, 18)
	out, errOut, code := c.run(deliverInput(r, nonce, []byte(token)), nil, "ask", "deliver", r.RequestID)
	if code != 2 || !strings.Contains(errOut, "looks like a secret") || strings.Contains(out+errOut, token) {
		t.Fatalf("token answer: exit %d stdout %q stderr %q", code, out, errOut)
	}
	if _, _, code := c.run("", nil, "ask", "cancel", r.RequestID, "--by", "app", "--nonce", nonce); code != 0 {
		t.Fatal("cancel")
	}
	if code := asker.wait(t, 20*time.Second); code != 3 || strings.Contains(asker.stdout.String()+asker.stderr.String(), token) {
		t.Fatalf("asker exit %d stdout %q stderr %q", code, asker.stdout, asker.stderr)
	}
}

func TestAskTimeoutExit4(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Planner")
	out, errOut, code := c.run("", nil, "ask", "text", "Anyone there?", "--session", sid, "--wait", "1s")
	if code != 4 || out != "" || !strings.Contains(errOut, "timed out after 1s") {
		t.Fatalf("exit %d stdout %q stderr %q, want 4 and a timeout line", code, out, errOut)
	}
}

func TestAskNoFallbackExit5(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Planner")
	out, errOut, code := c.run("", nil, "ask", "text", "Anyone there?", "--session", sid, "--no-fallback", "--fallback-after", "300ms")
	if code != 5 || out != "" || strings.Contains(errOut, "ask answer") {
		t.Fatalf("exit %d stdout %q stderr %q, want 5 without the fallback text", code, out, errOut)
	}
}

func TestAskFallbackTextPrintedOnce(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Planner")
	out, errOut, code := c.run("", nil, "ask", "secret", "conductor/NEW_KEY", "--session", sid, "--fallback-after", "200ms", "--wait", "2s")
	if code != 4 || out != "" {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	if n := strings.Count(errOut, "No AgentDeck app or TUI is showing this request"); n != 1 {
		t.Fatalf("fallback printed %d times:\n%s", n, errOut)
	}
	if !strings.Contains(errOut, "agent-deck -p ashesh ask answer ") || !strings.Contains(errOut, "(or: deck-secret set conductor/NEW_KEY)") {
		t.Fatalf("fallback text:\n%s", errOut)
	}
}

func TestAskSecretWithoutVaultExit5(t *testing.T) {
	parallelSubprocessTest(t)
	if _, err := os.Lstat("/usr/local/bin/deck-secret"); err == nil {
		t.Skip("this host has a vault; the no-vault path needs a host without one")
	}
	home := t.TempDir()
	id := addOpenTestSession(t, home, "Conductor")
	out, errOut, code := runAgentDeck(t, home, "ask", "secret", "conductor/GH_TOKEN", "--session", id)
	if code != 5 || out != "" || !strings.Contains(errOut, "no vault configured on this host") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errOut)
	}
}

func TestAskOneOpenRequestPerSessionCLI(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Planner")
	asker := c.start("ask", "text", "First?", "--session", sid)
	r := c.waitRequest(sid)
	out, errOut, code := c.run("", nil, "ask", "text", "Second?", "--session", sid)
	if code != 2 || out != "" || !strings.Contains(errOut, "already has an open request") {
		t.Fatalf("second ask: exit %d stdout %q stderr %q", code, out, errOut)
	}
	c.run("", nil, "ask", "cancel", r.RequestID, "--by", "app", "--unshown")
	asker.wait(t, 20*time.Second)
}

func TestAskSessionSpoofRefused(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	a := c.addSession("Agent")
	c.addSession("Conductor")
	out, errOut, code := c.run("", []string{"AGENTDECK_INSTANCE_ID=" + a}, "ask", "text", "q?", "--session", "Conductor")
	if code != 2 || out != "" || !strings.Contains(errOut, "another session") {
		t.Fatalf("spoofed --session: exit %d stdout %q stderr %q", code, out, errOut)
	}
}

func TestAskBadNameAndScopeRefused(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Conductor")
	for _, name := range []string{"conductor/../x", "other/GH_TOKEN", "GH_TOKEN"} {
		out, errOut, code := c.run("", nil, "ask", "secret", name, "--session", sid)
		if code != 2 || out != "" {
			t.Errorf("%s: exit %d stdout %q stderr %q", name, code, out, errOut)
		}
	}
}

func TestAskAnswerRefusesNonTTY(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	out, errOut, code := c.run("piped-value\n", nil, "ask", "answer", "0123456789abcdef")
	if code != 2 || !strings.Contains(errOut, "not a terminal") || strings.Contains(out+errOut, "piped-value") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errOut)
	}
}

func TestAskCommandHelp(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()
	for _, args := range [][]string{{"ask", "--help"}, {"ask", "-h"}, {"ask", "help"}, {"ask", "secret", "--help"}, {"ask", "deliver", "--help"}} {
		out, errOut, code := runAgentDeck(t, home, args...)
		if code != 0 || !strings.Contains(out, "Usage: agent-deck ask") {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, out, errOut)
		}
	}
	if _, errOut, code := runAgentDeck(t, home, "ask", "bogus"); code != 2 || !strings.Contains(errOut, "Usage: agent-deck ask") {
		t.Errorf("unknown subcommand: exit %d stderr %q", code, errOut)
	}
}

func TestAskEventsPublishGating(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()
	id := addOpenTestSession(t, home, "Conductor")
	for _, kind := range []string{"macapp.ask", "macapp.ask.ack", "macapp.ask.other"} {
		out, errOut, code := runAgentDeckStdin(t, home, `{"request_id":"0123456789abcdef","status":"cancelled"}`, "events", "publish", "--kind", kind, "--session", id, "--data-file", "-")
		if code == 0 || !strings.Contains(out+errOut, "only by agent-deck ask") {
			t.Errorf("%s: exit %d stdout %q stderr %q, want a refusal", kind, code, out, errOut)
		}
	}
	out, errOut, code := runAgentDeckStdin(t, home, `{"request_id":"0123456789abcdef","ask_event_id":"x","nonce":"0123456789abcdef","by":"app"}`, "events", "publish", "--kind", "macapp.ask.seen", "--session", id, "--data-file", "-")
	if code != 0 {
		t.Fatalf("macapp.ask.seen without [macapp] plugins: exit %d %q %q", code, out, errOut)
	}
	if _, err := remoteCommandArgs([]string{"events", "publish", "--kind", "macapp.ask.seen", "--session", "s1", "--data-file", "-"}); err != nil {
		t.Fatalf("remote forward of macapp.ask.seen: %v", err)
	}
	for _, kind := range []string{"macapp.ask", "macapp.ask.ack"} {
		if _, err := remoteCommandArgs([]string{"events", "publish", "--kind", kind, "--session", "s1", "--data-file", "-"}); err == nil {
			t.Errorf("remote forward of %s accepted", kind)
		}
	}
}

func TestAskCallerSessionFromParentChain(t *testing.T) {
	instances := []askCallerInstance{
		{ID: "a", TmuxSession: "agentdeck_a", Socket: ""},
		{ID: "b", TmuxSession: "agentdeck_b", Socket: "work"},
	}
	panes := map[int]askPaneRef{
		100: {Socket: "", Session: "agentdeck_a"},
		200: {Socket: "work", Session: "agentdeck_b"},
		300: {Socket: "", Session: "someone-else"},
	}
	tests := []struct {
		ancestors []int
		want      string
	}{
		{[]int{4000, 3000, 100, 1}, "a"},
		{[]int{4000, 200, 1}, "b"},
		{[]int{4000, 300, 100}, ""}, // the nearest pane is not an agent-deck session
		{[]int{4000, 5}, ""},
		{nil, ""},
	}
	for _, tt := range tests {
		if got := askCallerSession(tt.ancestors, panes, instances); got != tt.want {
			t.Errorf("ancestors %v: %q, want %q", tt.ancestors, got, tt.want)
		}
	}
	// This test process has a parent chain on Linux.
	if _, err := os.Stat("/proc/self/stat"); err == nil {
		chain := procAncestors(os.Getpid())
		if len(chain) < 2 || chain[0] != os.Getpid() || chain[1] != os.Getppid() {
			t.Fatalf("procAncestors = %v", chain)
		}
	}
}

// macAppDeliverStdin is the exact stdin the Mac app's AskDelivery.stdin
// builds (mac/Sources/AgentDeckSpike/AskDelivery.swift): header() is
// JSONSerialization with .sortedKeys and no other option, so keys come
// sorted, there are no spaces and "/" is escaped as "\/"; then one 0x0a;
// then the value bytes exactly (no trailing newline).
func macAppDeliverStdin(requestID, kind, name, nonce string, replace bool, value string) string {
	esc := strings.ReplaceAll(name, "/", `\/`)
	return fmt.Sprintf(`{"answered_by":"app","kind":%q,"name":"%s","nonce":%q,"replace":%t,"request_id":%q}`, kind, esc, nonce, replace, requestID) +
		"\n" + value
}

// Cross-language contract: the bytes the Mac app writes to `ask deliver`'s
// stdin are accepted and the value lands in the vault byte for byte; a
// choice answer the same way.
func TestAskDeliverAcceptsMacAppStdinBytes(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Conductor")
	asker := c.start("ask", "secret", "conductor/GH_TOKEN_DECKBOX", "--session", sid)
	r := c.waitRequest(sid)
	nonce := c.appSeen(r)
	value := "mac-" + randomHex(t, 12)
	stdin := macAppDeliverStdin(r.RequestID, "secret", "conductor/GH_TOKEN_DECKBOX", nonce, false, value)
	if !strings.Contains(stdin, `"name":"conductor\/GH_TOKEN_DECKBOX"`) {
		t.Fatalf("fixture does not escape the slash as JSONSerialization does: %s", stdin)
	}
	out, errOut, code := c.run(stdin, nil, "ask", "deliver", r.RequestID)
	if code != 0 || strings.TrimSpace(out) != `{"status":"stored","replaced":"no"}` || strings.Contains(out+errOut, value) {
		t.Fatalf("deliver of the Mac app's bytes: exit %d stdout %q stderr %q", code, out, errOut)
	}
	if v, ok := c.vaultValue("conductor/GH_TOKEN_DECKBOX"); !ok || v != value {
		t.Fatal("the vault did not get the Mac app's value byte for byte")
	}
	if code := asker.wait(t, 20*time.Second); code != 0 || asker.stdout.String() != "stored conductor/GH_TOKEN_DECKBOX (replaced=no, by app)\n" {
		t.Fatalf("asker exit %d stdout %q", code, asker.stdout)
	}

	sid2 := c.addSession("Planner")
	chooser := c.start("ask", "choice", "Which database?", "--option", "staging", "--option", "prod", "--session", sid2)
	r2 := c.waitRequest(sid2)
	nonce2 := c.appSeen(r2)
	if out, errOut, code := c.run(macAppDeliverStdin(r2.RequestID, "choice", "", nonce2, false, "prod"), nil, "ask", "deliver", r2.RequestID); code != 0 || strings.TrimSpace(out) != `{"status":"answered","replaced":"no"}` {
		t.Fatalf("deliver of the Mac app's choice: exit %d stdout %q stderr %q", code, out, errOut)
	}
	if code := chooser.wait(t, 20*time.Second); code != 0 || chooser.stdout.String() != "prod\n" {
		t.Fatalf("chooser exit %d stdout %q", code, chooser.stdout)
	}
}

// Security review 2: `ask cancel` with --by app|tui|terminal needs the
// nonce that UI announced; --by asker is not a CLI option; --unshown
// cancels a request no UI showed, and only then, and the record says so.
func TestAskCancelCLINeedsNonceOrUnshown(t *testing.T) {
	parallelSubprocessTest(t)
	c := newAskCLI(t)
	sid := c.addSession("Planner")
	asker := c.start("ask", "text", "Anyone there?", "--session", sid)
	r := c.waitRequest(sid)
	for _, by := range []string{"app", "tui", "terminal", "asker"} {
		if out, errOut, code := c.run("", nil, "ask", "cancel", r.RequestID, "--by", by); code != 2 || strings.Contains(out, "cancelled") {
			t.Fatalf("cancel --by %s without a nonce: exit %d stdout %q stderr %q, want a refusal", by, code, out, errOut)
		}
	}
	out, errOut, code := c.run("", nil, "ask", "cancel", r.RequestID, "--by", "app", "--unshown")
	if code != 0 || strings.TrimSpace(out) != `{"status":"cancelled"}` {
		t.Fatalf("cancel --unshown: exit %d stdout %q stderr %q", code, out, errOut)
	}
	if code := asker.wait(t, 20*time.Second); code != 3 || !strings.Contains(asker.stderr.String(), "cancelled by app") {
		t.Fatalf("asker exit %d stderr %q", code, asker.stderr)
	}
	shown, _, _ := c.run("", nil, "ask", "show", r.RequestID, "--json")
	if !strings.Contains(shown, `"cancelled_unshown":true`) {
		t.Fatalf("the record does not note the unshown cancel: %s", shown)
	}

	sid2 := c.addSession("Coder")
	asker2 := c.start("ask", "text", "Still there?", "--session", sid2)
	r2 := c.waitRequest(sid2)
	nonce := c.appSeen(r2)
	if out, errOut, code := c.run("", nil, "ask", "cancel", r2.RequestID, "--by", "app", "--unshown"); code != 2 {
		t.Fatalf("cancel --unshown of a shown request: exit %d stdout %q stderr %q, want 2", code, out, errOut)
	}
	if out, errOut, code := c.run("", nil, "ask", "cancel", r2.RequestID, "--by", "app", "--nonce", nonce); code != 0 {
		t.Fatalf("cancel with the app's nonce: exit %d stdout %q stderr %q", code, out, errOut)
	}
	if code := asker2.wait(t, 20*time.Second); code != 3 {
		t.Fatalf("asker exit %d, want 3", code)
	}
}
