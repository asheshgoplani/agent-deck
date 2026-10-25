package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// askInTestContainer reports whether this test runs in the test container
// (AGENTS.md: /.dockerenv, /run/.containerenv or AGENTDECK_TEST_CONTAINER=1),
// where tmux and python3 are installed.
func askInTestContainer() bool {
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return os.Getenv("AGENTDECK_TEST_CONTAINER") == "1"
}

// requireAskTools fails the test when a tool the leak checks need is
// missing in the test container (a silent skip there would pass a leak
// test that checked nothing); elsewhere it skips.
func requireAskTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			if askInTestContainer() {
				t.Fatalf("%s is missing in the test container: the leak check cannot run", tool)
			}
			t.Skipf("requires %s", tool)
		}
	}
}

// askRedact replaces the sentinel in s, so no failure message prints it.
func askRedact(s string, sentinel []byte) string {
	return strings.ReplaceAll(s, string(sentinel), "<SENTINEL>")
}

// Acceptance line 1, through a real agent-deck session. The asker runs as
// the requesting session's tool call: in a window of that session's tmux
// session (so the /proc chain verifies it as the caller), with
// AGENTDECK_DEBUG=1. Its real stdout and stderr then take the route a
// Claude tool call's output takes: the Claude transcript (tool_use and
// tool_result rows, as Claude Code writes them), the PreToolUse and
// PostToolUse hook payloads through `agent-deck hook-handler`, the Stop
// hook with last_assistant_message, the notifier (`notify-daemon --once`)
// that turns the finished turn into a record in the parent's inbox, and
// the parent's UserPromptSubmit hook that drains that inbox into its
// context (the conductor route). A random sentinel goes in through `ask
// deliver` as the app sends it. Then every file under the env root (hook
// state and history, events log, state.db, inbox, comms spool, transcript,
// debug and cost logs, ask dir, config), every hook and CLI output, the
// panes of the asker, the child and the parent must not contain it; only
// the fake vault's value file may. Positive controls prove each consumer
// ran on the asker's real output.
func TestAskSecretNeverLeaksThroughAgentSession(t *testing.T) {
	parallelSubprocessTest(t)
	requireAskTools(t, "tmux", "python3")
	e := newDeliveryEnv(t)
	e.env = append(e.env, "AGENTDECK_DEBUG=1")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(e.root, "vault-store")
	vault := filepath.Join(e.root, "vault", "deck-secret")
	for _, d := range []string{store, filepath.Dir(vault)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, vault, fmt.Sprintf(askFakeVault, store), 0o700)
	vaultEnv := append(append([]string{}, e.env...), askHelperVaultEnv+"="+vault)

	sentinel := []byte("SENTINEL-" + randomHex(t, 16))
	const name = "conductor/LEAK_SESSION"
	valueFile := filepath.Join(store, "conductor_LEAK_SESSION")
	var outputs []string // every hook / CLI output, grepped at the end
	record := func(what, s string) { outputs = append(outputs, what+"\x00"+s) }

	parent := e.startFake("parent", "idle", "")
	child := e.startFake("child", "busy", parent.id)
	e.setPhase(child, "busy")
	record("notify-daemon baseline", e.mustRun("notify-daemon", "--once"))

	hook := func(f *fakeTarget, payload map[string]any, extraEnv ...string) string {
		t.Helper()
		b, _ := json.Marshal(payload)
		env := append([]string{"AGENTDECK_INSTANCE_ID=" + f.id, "AGENTDECK_PROFILE=" + deliveryE2EProfile}, extraEnv...)
		out, errOut, code := e.run(string(b), env, "hook-handler")
		if code != 0 {
			t.Fatalf("hook-handler %v: exit %d %s", payload["hook_event_name"], code, askRedact(errOut, sentinel))
		}
		record(fmt.Sprintf("hook-handler %v", payload["hook_event_name"]), out+errOut)
		return out
	}

	// The tool call: the asker in a window of the child's tmux session.
	toolCmd := "agent-deck ask secret " + name + " --reason 'session leak probe'"
	outF, errF, codeF := filepath.Join(e.root, "asker.out"), filepath.Join(e.root, "asker.err"), filepath.Join(e.root, "asker.code")
	var envLine []string
	for _, kv := range append(append([]string{}, vaultEnv...), "AGENTDECK_INSTANCE_ID="+child.id) {
		envLine = append(envLine, shellQuote(kv))
	}
	script := "env -i " + strings.Join(envLine, " ") + " " + shellQuote(self) + " -p " + deliveryE2EProfile +
		" ask secret " + name + " --reason 'session leak probe' --session " + child.id +
		" >" + shellQuote(outF) + " 2>" + shellQuote(errF) + "; echo $? >" + shellQuote(codeF) +
		"; cat " + shellQuote(outF) + " " + shellQuote(errF) + "; echo ask-done; sleep 120"
	tmuxCmd := func(args ...string) *exec.Cmd {
		cmd := exec.Command("tmux", args...)
		cmd.Env = e.env
		return cmd
	}
	hook(child, map[string]any{"hook_event_name": "PreToolUse", "session_id": child.claudeSID, "cwd": child.project,
		"transcript_path": child.transcript, "tool_name": "Bash", "tool_input": map[string]string{"command": toolCmd}},
		"AGENTDECK_PRETOOL_SYNC=1")
	if out, err := tmuxCmd("new-window", "-d", "-t", child.tmux+":", "-n", "askprobe", "sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-window in the child's session: %v %s", err, out)
	}
	askPane := child.tmux + ":askprobe"

	// The app's side: seen with a nonce, then deliver with the sentinel.
	var req askListed
	var verified bool
	deadline := time.Now().Add(30 * time.Second)
	for req.RequestID == "" {
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(errF)
			t.Fatalf("the asker in the child's pane never announced a request; stderr %q", b)
		}
		out, _, _ := e.run("", nil, "ask", "list", "--json")
		var list []struct {
			askListed
			CallerVerified bool `json:"caller_verified"`
		}
		if json.Unmarshal([]byte(out), &list) == nil {
			for _, r := range list {
				if r.SessionID == child.id && r.Name == name && r.AskEventID != "" {
					req, verified = r.askListed, r.CallerVerified
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !verified {
		t.Fatal("the asker in the child's tmux session was not verified as its caller")
	}
	nonce := randomHex(t, 8)
	seen, _ := json.Marshal(map[string]string{"request_id": req.RequestID, "ask_event_id": req.AskEventID, "nonce": nonce, "by": "app"})
	if out, errOut, code := e.run(string(seen), nil, "events", "publish", "--kind", "macapp.ask.seen", "--session", child.id, "--data-file", "-"); code != 0 {
		t.Fatalf("events publish seen: %d %s %s", code, out, errOut)
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		out, _, _ := e.run("", nil, "ask", "show", req.RequestID, "--json")
		var shown askListed
		if json.Unmarshal([]byte(out), &shown) == nil && len(shown.SeenBy) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the asker never recorded the app's seen frame")
		}
		time.Sleep(100 * time.Millisecond)
	}
	deliver := exec.Command(self, "-p", deliveryE2EProfile, "ask", "deliver", req.RequestID)
	deliver.Env = vaultEnv
	deliver.Stdin = strings.NewReader(deliverInput(req, nonce, append(append([]byte{}, sentinel...), '\n')))
	dOut, err := deliver.CombinedOutput()
	if err != nil || strings.TrimSpace(string(dOut)) != `{"status":"stored","replaced":"no"}` {
		t.Fatalf("deliver: %v %s", err, askRedact(string(dOut), sentinel))
	}
	record("deliver", string(dOut))
	deadline = time.Now().Add(30 * time.Second)
	var code []byte
	for {
		if code, _ = os.ReadFile(codeF); len(code) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the asker never finished")
		}
		time.Sleep(100 * time.Millisecond)
	}
	askerOut, _ := os.ReadFile(outF)
	askerErr, _ := os.ReadFile(errF)
	if strings.TrimSpace(string(code)) != "0" || !strings.HasPrefix(string(askerOut), "stored "+name+" (replaced=no, by app)") {
		t.Fatalf("asker exit %s stdout %q stderr %q", code, askRedact(string(askerOut), sentinel), askRedact(string(askerErr), sentinel))
	}
	// Anything more is reported, not fatal: the output still takes the
	// whole route below, so a leak in it is found in every store it reaches.
	if string(askerOut) != "stored "+name+" (replaced=no, by app)\n" {
		t.Errorf("asker stdout %q, want only the stored line", askRedact(string(askerOut), sentinel))
	}
	if v, err := os.ReadFile(valueFile); err != nil || !bytes.Equal(v, sentinel) {
		t.Fatal("the fake vault did not get the sentinel")
	}

	// The tool call's output takes Claude's routes: transcript rows, then
	// PostToolUse with tool_response, the reply, Stop.
	toolUseID := "toolu_" + randomHex(t, 8)
	output := string(askerOut) + string(askerErr)
	appendTranscript := func(rows ...map[string]any) {
		t.Helper()
		f, err := os.OpenFile(child.transcript, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, r := range rows {
			b, _ := json.Marshal(r)
			if _, err := f.Write(append(b, '\n')); err != nil {
				t.Fatal(err)
			}
		}
	}
	stamp := func() string { return time.Now().UTC().Format(time.RFC3339Nano) }
	appendTranscript(
		map[string]any{"type": "assistant", "uuid": "tu-" + toolUseID, "timestamp": stamp(), "sessionId": child.claudeSID,
			"message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": toolUseID, "name": "Bash", "input": map[string]string{"command": toolCmd}}}}},
		map[string]any{"type": "user", "uuid": "tr-" + toolUseID, "timestamp": stamp(), "sessionId": child.claudeSID,
			"message":       map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": toolUseID, "content": output, "is_error": false}}},
			"toolUseResult": map[string]any{"stdout": string(askerOut), "stderr": string(askerErr), "interrupted": false, "isImage": false}},
	)
	hook(child, map[string]any{"hook_event_name": "PostToolUse", "session_id": child.claudeSID, "cwd": child.project,
		"transcript_path": child.transcript, "tool_name": "Bash", "tool_input": map[string]string{"command": toolCmd},
		"tool_response": map[string]any{"stdout": string(askerOut), "stderr": string(askerErr), "interrupted": false, "isImage": false}})
	reply := "The owner answered: " + strings.TrimSpace(string(askerOut))
	child.reply(t, reply)
	child.phaseN++
	phaseID := fmt.Sprintf("idle-%d", child.phaseN)
	writeFile(t, child.transcript+".input-control.json", fmt.Sprintf(`{"id":%q,"phase":"idle"}`, phaseID), 0o600)
	issue2530WaitFor(t, 5*time.Second, "fake phase "+phaseID, func() bool {
		var st struct{ ID string }
		b, _ := os.ReadFile(child.transcript + ".input-state.json")
		return json.Unmarshal(b, &st) == nil && st.ID == phaseID
	})
	stopActive := false
	hook(child, map[string]any{"hook_event_name": "Stop", "session_id": child.claudeSID, "cwd": child.project,
		"transcript_path": child.transcript, "stop_hook_active": stopActive, "last_assistant_message": reply})
	record("notify-daemon after the turn", e.mustRun("notify-daemon", "--once"))

	// The parent's inbox holds the child's turn with the asker's result.
	peek := e.mustRun("inbox", "peek", parent.id, "--json")
	record("inbox peek", peek)
	if !strings.Contains(peek, "stored "+name) {
		t.Fatalf("the parent's inbox does not carry the child's turn with the asker's output:\n%s", askRedact(peek, sentinel))
	}
	// The conductor route: the parent's next prompt drains it into context.
	drained := hook(parent, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": parent.claudeSID,
		"cwd": parent.project, "transcript_path": parent.transcript, "prompt": "continue"})
	if !strings.Contains(drained, "stored "+name) {
		t.Fatalf("the parent's UserPromptSubmit hook did not inject the child's turn:\n%s", askRedact(drained, sentinel))
	}

	// Panes: the asker's window, the child and the parent.
	var askPaneText []byte
	deadline = time.Now().Add(10 * time.Second)
	for !bytes.Contains(askPaneText, []byte("ask-done")) {
		if time.Now().After(deadline) {
			t.Fatalf("the asker's pane never showed its output:\n%s", askRedact(string(askPaneText), sentinel))
		}
		askPaneText, _ = tmuxCmd("capture-pane", "-p", "-J", "-S", "-", "-t", askPane).Output()
		time.Sleep(100 * time.Millisecond)
	}
	if !bytes.Contains(askPaneText, []byte("stored "+name+" (replaced=no, by app)")) {
		t.Fatalf("the asker's pane does not show its result:\n%s", askRedact(string(askPaneText), sentinel))
	}
	record("asker pane", string(askPaneText))
	for _, f := range []*fakeTarget{child, parent} {
		pane, _ := tmuxCmd("capture-pane", "-p", "-J", "-S", "-", "-t", f.tmux).Output()
		record("pane "+f.tmux, string(pane))
	}
	record("asker stdout", string(askerOut))
	record("asker stderr", string(askerErr))

	for _, o := range outputs {
		what, text, _ := strings.Cut(o, "\x00")
		if strings.Contains(text, string(sentinel)) {
			t.Errorf("sentinel in %s", what)
		}
	}

	// Every file under the env root. Positive controls: the child's hook
	// state, the ack in the events log, the asker's result in the
	// transcript and the inbox, and ask lines in the debug log.
	var walked int
	var hookState, ackInEvents, resultInFiles, askDebugLines bool
	err = filepath.WalkDir(e.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a socket dir or a file removed mid-walk
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		walked++
		base := filepath.Base(path)
		if base == child.id+".json" && strings.Contains(path, "hooks") {
			hookState = true
		}
		if bytes.Contains(b, []byte("macapp.ask.ack")) {
			ackInEvents = true
		}
		if path != outF && bytes.Contains(b, []byte("stored "+name)) {
			resultInFiles = true
		}
		if base == "debug.log" && bytes.Contains(b, []byte(`"component":"ask"`)) && bytes.Contains(b, []byte(req.RequestID)) {
			askDebugLines = true
		}
		if bytes.Contains(b, sentinel) && path != valueFile {
			rel, _ := filepath.Rel(e.root, path)
			t.Errorf("sentinel found in %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hookState || !ackInEvents || !resultInFiles || walked < 20 {
		t.Fatalf("walk covered %d files; hook state %v, ack in the events log %v, asker result in the transcript/inbox %v", walked, hookState, ackInEvents, resultInFiles)
	}
	if !askDebugLines {
		t.Fatal("AGENTDECK_DEBUG=1: no debug.log has the ask lines of this request; the debug log was not checked")
	}
}
