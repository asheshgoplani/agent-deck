package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// End-to-end delivery tests: the real binary, a real tmux server (the
// isolated #2530 env), and a Claude-like fake agent in the pane
// (testdata/claude_like_fake.py, started by a fake `claude` command). The
// binary fires the hooks Claude would fire (`hook-handler`), so busy and idle
// come from the same hook files production reads.
//
// They pin the delivery contracts that came back more than once:
//   - a send to a busy target is reported queued, never failed, and reaches
//     the target exactly once (#2033, #2273);
//   - a foreign draft in the target's composer is never wiped or typed onto,
//     and a queued send waits behind it, then lands once (#2104, #2263);
//   - a child's finished turn reaches its parent's durable inbox once, across
//     notifier restarts, and drains once (#2007, #2028 reverted, #2039, #2240).

const deliveryE2EProfile = "e2edeliv"

// deliveryFakeClaude is the configured `claude` command. It runs the fake
// with its transcript where Claude keeps it ($HOME/.claude/projects/<cwd
// encoded>/<session id>.jsonl), so the binary finds the transcript the way
// it finds Claude's. The start phase is read from .fake-phase in the project.
const deliveryFakeClaude = `#!/bin/sh
sid=nosid
while [ $# -gt 0 ]; do
  case $1 in --session-id) sid=$2; shift 2 ;; *) shift ;; esac
done
here=$(cd "$(dirname "$0")" && pwd -P)
cwd=$(pwd -P)
proj="$HOME/.claude/projects/$(printf %s "$cwd" | sed 's/[^a-zA-Z0-9-]/-/g')"
mkdir -p "$proj" "$cwd/.fake"
t="$proj/$sid.jsonl"
: >> "$t"
phase=$(cat "$cwd/.fake-phase" 2>/dev/null || echo idle)
printf '{"id":"%s","phase":"%s"}' "$phase" "$phase" > "$t.input-control.json"
printf '{"%s":"%s"}' "$cwd" "$t" > "$cwd/.fake/fake-running.json"
cp "$here/claude_like_fake.py" "$cwd/.fake/claude"
printf %s "$t" > "$cwd/.fake/transcript"
exec python3 -I "$cwd/.fake/claude"
`

type deliveryEnv struct {
	*issue2530Env
}

type fakeTarget struct {
	id, claudeSID, tmux, project, transcript string
	phaseN                                   int
}

func newDeliveryEnv(t *testing.T) *deliveryEnv {
	t.Helper()
	for _, tool := range []string{"tmux", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("requires %s", tool)
		}
	}
	e := &deliveryEnv{newIssue2530Env(t)}
	e.env = append(e.env,
		"AGENTDECK_SEND_WORKER_POLL=200ms",
		"AGENTDECK_SEND_RETRY_BACKOFF_MAX=500ms",
		"AGENTDECK_SEND_LAND_WINDOW=2s",
	)
	t.Cleanup(e.killDetached)
	bin := filepath.Join(e.root, "fakebin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "claude_like_fake.py"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(bin, "claude_like_fake.py"), string(src), 0o600)
	writeFile(t, filepath.Join(bin, "claude"), deliveryFakeClaude, 0o700)
	cfgDir := filepath.Join(e.root, "config", "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cfgDir, "config.toml"), fmt.Sprintf("[claude]\ncommand = %q\n", filepath.Join(bin, "claude")), 0o600)
	return e
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// run runs the binary in the env's profile and returns its output and exit
// code; it never fails the test on a non-zero exit.
func (e *deliveryEnv) run(stdin string, extraEnv []string, args ...string) (string, string, int) {
	e.t.Helper()
	cmd := exec.Command(e.bin, append([]string{"-p", deliveryE2EProfile}, args...)...)
	cmd.Env = append(append([]string{}, e.env...), extraEnv...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			e.t.Fatalf("agent-deck %s: %v", strings.Join(args, " "), err)
		}
		code = exitErr.ExitCode()
	}
	return out.String(), errb.String(), code
}

func (e *deliveryEnv) mustRun(args ...string) string {
	e.t.Helper()
	out, errOut, code := e.run("", nil, args...)
	if code != 0 {
		e.t.Fatalf("agent-deck %s: exit %d\n%s\n%s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

// startFake adds a Claude session (with parent, or none) running the fake in
// phase, starts it, fires SessionStart, and waits for its first frame.
func (e *deliveryEnv) startFake(title, phase, parent string) *fakeTarget {
	e.t.Helper()
	f := &fakeTarget{project: filepath.Join(e.root, "p-"+title)}
	if err := os.MkdirAll(f.project, 0o700); err != nil {
		e.t.Fatal(err)
	}
	writeFile(e.t, filepath.Join(f.project, ".fake-phase"), phase, 0o600)
	link := []string{"--no-parent"}
	if parent != "" {
		link = []string{"--parent", parent}
	}
	var added struct {
		ID string `json:"id"`
	}
	out := e.mustRun(append(append([]string{"add", "-t", title, "-c", "claude"}, link...), "--json", f.project)...)
	if err := json.Unmarshal([]byte(out), &added); err != nil || added.ID == "" {
		e.t.Fatalf("add %s: %v\n%s", title, err, out)
	}
	f.id = added.ID
	e.mustRun("session", "start", f.id, "--json")
	var shown struct {
		TmuxSession string `json:"tmux_session"`
		ClaudeSID   string `json:"claude_session_id"`
	}
	if err := json.Unmarshal([]byte(e.mustRun("session", "show", f.id, "--json")), &shown); err != nil || shown.TmuxSession == "" {
		e.t.Fatalf("session show %s: %v", title, err)
	}
	f.tmux, f.claudeSID = shown.TmuxSession, shown.ClaudeSID
	issue2530WaitFor(e.t, 15*time.Second, title+" fake to start", func() bool {
		b, err := os.ReadFile(filepath.Join(f.project, ".fake", "transcript"))
		f.transcript = string(b)
		return err == nil && f.transcript != ""
	})
	want := "All four steps ran."
	if phase == "busy" {
		want = "Thundering"
	}
	e.waitPane(f, title+" first frame", func(p string) bool { return strings.Contains(p, want) && strings.Contains(p, "❯") })
	e.hook(f, "SessionStart")
	return f
}

// hook fires a Claude hook event for f through the real hook handler.
func (e *deliveryEnv) hook(f *fakeTarget, event string) {
	e.t.Helper()
	payload, _ := json.Marshal(map[string]string{"hook_event_name": event, "session_id": f.claudeSID, "cwd": f.project})
	_, errOut, code := e.run(string(payload), []string{"AGENTDECK_INSTANCE_ID=" + f.id, "AGENTDECK_PROFILE=" + deliveryE2EProfile}, "hook-handler")
	if code != 0 {
		e.t.Fatalf("hook-handler %s: exit %d %s", event, code, errOut)
	}
}

// setPhase switches the fake to busy (a turn runs) or idle, and fires the
// hook Claude fires on that edge.
func (e *deliveryEnv) setPhase(f *fakeTarget, phase string) {
	e.t.Helper()
	f.phaseN++
	id := fmt.Sprintf("%s-%d", phase, f.phaseN)
	writeFile(e.t, f.transcript+".input-control.json", fmt.Sprintf(`{"id":%q,"phase":%q}`, id, phase), 0o600)
	issue2530WaitFor(e.t, 5*time.Second, "fake phase "+id, func() bool {
		var st struct{ ID string }
		b, _ := os.ReadFile(f.transcript + ".input-state.json")
		return json.Unmarshal(b, &st) == nil && st.ID == id
	})
	if phase == "busy" {
		e.hook(f, "UserPromptSubmit")
	} else {
		e.hook(f, "Stop")
	}
}

func (e *deliveryEnv) paneText(f *fakeTarget) string {
	capture := exec.Command("tmux", "capture-pane", "-p", "-t", f.tmux)
	capture.Env = e.env
	out, _ := capture.Output()
	return string(out)
}

func (e *deliveryEnv) waitPane(f *fakeTarget, what string, ok func(string) bool) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		p := e.paneText(f)
		if ok(p) {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("pane never showed %s:\n%s", what, p)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// draft is what the fake's composer holds, from its own state file.
func (f *fakeTarget) draft(t *testing.T) string {
	t.Helper()
	var st struct{ Draft string }
	b, _ := os.ReadFile(f.transcript + ".input-state.json")
	_ = json.Unmarshal(b, &st)
	return st.Draft
}

// received counts the inputs the fake took that contain token: submitted
// prompts (user rows) and messages queued during a turn (enqueue rows).
func (f *fakeTarget) received(t *testing.T, token string) int {
	t.Helper()
	b, err := os.ReadFile(f.transcript)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		var rec struct {
			Type, Operation, Content string
			Message                  struct{ Content any }
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		text, _ := rec.Message.Content.(string)
		if rec.Type == "queue-operation" && rec.Operation == "enqueue" {
			text = rec.Content
		} else if rec.Type != "user" {
			continue
		}
		if strings.Contains(text, token) {
			n++
		}
	}
	return n
}

// sendStatus reads a queued send's record.
func (e *deliveryEnv) sendStatus(id string) sendRecordJSON {
	e.t.Helper()
	var st sendRecordJSON
	out := e.mustRun("session", "send-status", id, "--json")
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		e.t.Fatalf("send-status JSON: %v\n%s", err, out)
	}
	return st
}

// queue sends msg with --queue and checks the immediate reply is queued.
func (e *deliveryEnv) queue(f *fakeTarget, msg string) string {
	e.t.Helper()
	out, errOut, code := e.run(msg, nil, "session", "send", f.id, "--message-file", "-", "--queue", "--json")
	var rec sendRecordJSON
	if code != 0 || json.Unmarshal([]byte(out), &rec) != nil || rec.SendID == "" || rec.State != "queued" || rec.Verdict != "queued" {
		e.t.Fatalf("--queue reply: exit %d %s %s; want exit 0 and a queued record", code, out, errOut)
	}
	return rec.SendID
}

// waitSettled waits for a queued send to settle and fails on a failed one
// or when the target got msg more than once.
func (e *deliveryEnv) waitSettled(f *fakeTarget, sendID, msg string, within time.Duration) sendRecordJSON {
	e.t.Helper()
	deadline := time.Now().Add(within)
	for {
		if n := f.received(e.t, msg); n > 1 {
			e.t.Fatalf("queued send reached the target %d times, want 1 (record %+v)", n, e.sendStatus(sendID))
		}
		st := e.sendStatus(sendID)
		if st.State == "failed" {
			e.t.Fatalf("queued send reported failed: %+v", st)
		}
		if st.Settled || st.State == "landed" {
			return st
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("queued send never settled within %v: %+v", within, st)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func deliveryToken(name string) string {
	return fmt.Sprintf("E2E_%s_%d", name, time.Now().UnixNano())
}

// TestDeliveryE2E_SendToBusyClaudeIsQueuedAndLandsOnce: a mid-turn target
// takes a message into its own queue. The CLI reports it delivered (queued,
// or sent once the transcript shows it absorbed) with exit 0 (#2273: it said
// "NOT delivered", exit 1, and callers resent), and the target receives it
// exactly once. A --queue send to the same busy target is
// recorded queued, is typed once by the worker and settles without failure
// after the turn ends.
func TestDeliveryE2E_SendToBusyClaudeIsQueuedAndLandsOnce(t *testing.T) {
	parallelSubprocessTest(t)
	e := newDeliveryEnv(t)
	f := e.startFake("busy-target", "busy", "")
	e.setPhase(f, "busy")

	direct := deliveryToken("BUSY_DIRECT")
	out, errOut, code := e.run("", nil, "session", "send", f.id, direct+" please look at this mid-turn")
	if code != 0 || strings.Contains(out+errOut, "NOT delivered") {
		t.Fatalf("send to a busy target: exit %d stdout %q stderr %q; want exit 0, reported delivered or queued", code, out, errOut)
	}
	issue2530WaitFor(t, 5*time.Second, "the busy target to take the direct message", func() bool { return f.received(t, direct) > 0 })

	queued := deliveryToken("BUSY_QUEUED")
	sendID := e.queue(f, queued+" queued while the turn runs")
	issue2530WaitFor(t, 20*time.Second, "the worker to type the queued message", func() bool {
		if st := e.sendStatus(sendID); st.State == "failed" {
			t.Fatalf("queued send to a busy target reported failed: %+v", st)
		}
		return f.received(t, queued) > 0
	})
	e.setPhase(f, "idle")
	st := e.waitSettled(f, sendID, queued, 20*time.Second)
	if st.Attempts != 1 {
		t.Fatalf("queued send attempted %d times, want 1: %+v", st.Attempts, st)
	}
	time.Sleep(time.Second) // a duplicate would be typed by now (poll 200ms)
	if n := f.received(t, direct); n != 1 {
		t.Fatalf("direct send reached the target %d times, want 1", n)
	}
	if n := f.received(t, queued); n != 1 {
		t.Fatalf("queued send reached the target %d times, want 1 (record %+v)", n, st)
	}
}

// TestDeliveryE2E_ForeignDraftIsNeverWiped: an operator is typing in the
// target's composer. A send refuses without touching the draft (#2263: the
// guard pressed Ctrl-C, which wipes it and, twice, exits Claude), and a
// --queue send waits behind the draft, stays queued (not failed), and lands
// once after the operator submits their own text.
func TestDeliveryE2E_ForeignDraftIsNeverWiped(t *testing.T) {
	parallelSubprocessTest(t)
	e := newDeliveryEnv(t)
	f := e.startFake("draft-target", "idle", "")
	e.setPhase(f, "idle")
	draft := deliveryToken("OPERATOR_DRAFT") + " half written note"
	keys := exec.Command("tmux", "send-keys", "-t", f.tmux, "-l", draft)
	keys.Env = e.env
	if out, err := keys.CombinedOutput(); err != nil {
		t.Fatalf("type the draft: %v %s", err, out)
	}
	e.waitPane(f, "the operator draft", func(p string) bool { return strings.Contains(p, "❯ "+draft) })

	refused := deliveryToken("REFUSED")
	out, errOut, code := e.run("", nil, "session", "send", f.id, refused, "--no-wait", "--json")
	var res struct{ Delivery string }
	_ = json.Unmarshal([]byte(out), &res)
	if code == 0 || res.Delivery != deliveryComposerBlocked {
		t.Fatalf("send onto a foreign draft: exit %d delivery %q (%s %s); want a composer_blocked refusal", code, res.Delivery, out, errOut)
	}
	if got := f.draft(t); got != draft {
		t.Fatalf("composer holds %q after the refused send, want the operator draft %q", got, draft)
	}

	queued := deliveryToken("BEHIND_DRAFT")
	sendID := e.queue(f, queued)
	issue2530WaitFor(t, 30*time.Second, "the worker to try twice behind the draft", func() bool {
		st := e.sendStatus(sendID)
		if st.State == "failed" {
			t.Fatalf("queued send failed behind a draft: %+v", st)
		}
		return st.Attempts >= 2
	})
	// typing is the worker's attempt in flight; it must end refused, untyped.
	if st := e.sendStatus(sendID); st.State != "queued" && st.State != "typing" {
		t.Fatalf("send behind a draft is %q, want still queued: %+v", st.State, st)
	}
	if got := f.draft(t); got != draft {
		t.Fatalf("composer holds %q while a send waits, want the operator draft %q", got, draft)
	}
	if n := f.received(t, refused) + f.received(t, queued); n != 0 {
		t.Fatalf("a refused message reached the target %d time(s)", n)
	}

	// The operator submits their draft; the queued send then lands once.
	enter := exec.Command("tmux", "send-keys", "-t", f.tmux, "Enter")
	enter.Env = e.env
	if out, err := enter.CombinedOutput(); err != nil {
		t.Fatalf("submit the draft: %v %s", err, out)
	}
	issue2530WaitFor(t, 5*time.Second, "the operator draft to be submitted", func() bool { return f.received(t, draft) == 1 })
	e.hook(f, "Stop")
	issue2530WaitFor(t, 30*time.Second, "the queued send to reach the target", func() bool { return f.received(t, queued) > 0 })
	st := e.waitSettled(f, sendID, queued, 20*time.Second)
	time.Sleep(time.Second)
	if n := f.received(t, queued); n != 1 {
		t.Fatalf("queued send reached the target %d times, want 1 (record %+v)", n, st)
	}
	if n := f.received(t, draft); n != 1 {
		t.Fatalf("operator draft submitted %d times, want 1", n)
	}
	if n := f.received(t, refused); n != 0 {
		t.Fatalf("the refused send reached the target %d time(s) after all", n)
	}
}

// pendingFrom reads parent's inbox through the CLI without consuming it and
// returns the records for child.
func (e *deliveryEnv) pendingFrom(parent, child string) []map[string]any {
	e.t.Helper()
	return childRecords(e.t, e.mustRun("inbox", "peek", parent, "--json"), child)
}

func childRecords(t *testing.T, out, child string) []map[string]any {
	t.Helper()
	var events []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])), &events); err != nil {
		t.Fatalf("inbox JSON: %v\n%s", err, out)
	}
	var mine []map[string]any
	for _, ev := range events {
		if ev["child_session_id"] == child {
			mine = append(mine, ev)
		}
	}
	return mine
}

// drainedFrom drains parent's inbox through the CLI and returns the records
// for child.
func (e *deliveryEnv) drainedFrom(parent, child string) []map[string]any {
	e.t.Helper()
	return childRecords(e.t, e.mustRun("inbox", "drain", parent, "--json"), child)
}

// reply appends the assistant record Claude writes at the end of a turn.
func (f *fakeTarget) reply(t *testing.T, text string) {
	t.Helper()
	rec, _ := json.Marshal(map[string]any{
		"type": "assistant", "uuid": fmt.Sprintf("reply-%d", time.Now().UnixNano()),
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "sessionId": f.claudeSID,
		"message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": text}}},
	})
	out, err := os.OpenFile(f.transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := out.Write(append(rec, '\n')); err != nil {
		t.Fatal(err)
	}
}

// TestDeliveryE2E_ChildTurnReachesParentInboxOnceAcrossRestarts: a child's
// turn ends (Stop hook). Each notifier pass is a separate `notify-daemon
// --once` process, so every pass is a restart. The pass after the edge
// commits one running->waiting record to the parent's durable inbox; a
// second pass before the drain and another after it add nothing (#2240
// re-emitted on every restart; #2028 was reverted for losing replay after a
// restart); the drain delivers it once and a second drain is empty.
func TestDeliveryE2E_ChildTurnReachesParentInboxOnceAcrossRestarts(t *testing.T) {
	parallelSubprocessTest(t)
	e := newDeliveryEnv(t)
	parent := e.startFake("parent", "idle", "")
	child := e.startFake("child", "busy", parent.id)
	e.setPhase(child, "busy")
	e.mustRun("notify-daemon", "--once") // baseline: the child is running

	answer := deliveryToken("CHILD_REPLY")
	child.reply(t, answer+" the task is done")
	e.setPhase(child, "idle")
	e.mustRun("notify-daemon", "--once") // a new process sees the finished turn
	if n := len(e.pendingFrom(parent.id, child.id)); n != 1 {
		t.Fatalf("after the pass that saw the turn the parent inbox holds %d records for it, want 1", n)
	}
	e.mustRun("notify-daemon", "--once") // and another one must not repeat it
	if n := len(e.pendingFrom(parent.id, child.id)); n != 1 {
		t.Fatalf("a second notifier pass left %d records for one turn in the parent inbox, want 1", n)
	}

	got := e.drainedFrom(parent.id, child.id)
	if len(got) != 1 {
		t.Fatalf("parent inbox held %d records for the child's turn, want exactly 1: %v", len(got), got)
	}
	if got[0]["from_status"] != "running" || got[0]["to_status"] != "waiting" {
		t.Fatalf("record is %v -> %v, want running -> waiting: %v", got[0]["from_status"], got[0]["to_status"], got[0])
	}
	if raw, _ := json.Marshal(got[0]); !strings.Contains(string(raw), answer) {
		t.Fatalf("record does not carry the child's reply %q: %s", answer, raw)
	}

	e.mustRun("notify-daemon", "--once") // a restart after the drain
	if again := e.drainedFrom(parent.id, child.id); len(again) != 0 {
		t.Fatalf("second drain returned %d record(s) for a turn already drained: %v", len(again), again)
	}
}
