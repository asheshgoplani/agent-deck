package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestAcceptance2473_SmallWorkflowLifecycle is the acceptance test for issue
// #2473 ("a running workflow means a running session"). It drives ONE Claude
// session through the whole lifecycle of a small Workflow with two trivial
// agents, using the live captures of Claude Code 2.1.288 running exactly that
// (the probe-two-agents workflow: two agents that each sleep and return
// "done"):
//
//   - pane frames: internal/tmux/testdata/background_work/workflow-*.txt
//   - transcript:  internal/session/testdata/background_work/probe-workflow-agent-bash.jsonl
//   - hook file:   what Claude's UserPromptSubmit / Stop hooks write
//
// No real claude runs. A real tmux pane redraws each captured frame, the
// transcript sits where the session resolves it, and every poll reads the
// status through the code `session show --json` runs
// (sessionShowStatusFields) and the code `list --json` runs (buildListJSON)
// on the same instance, then once more from a fresh process (the row
// reloaded from storage, as every CLI invocation starts). Every surface must
// agree on every poll:
//
//  1. started:  prompt submitted, launching turn busy      running / running
//  2. running:  turn ended (Stop), workflow at 0/2, 1/2,    running / background-work
//     its agents reporting into the queue         + background_work {workflow, probe-two-agents, n/2}
//  3. finished: terminal task notification, summary turn   waiting / idle-at-empty-prompt, no background_work
//     ended (Stop): on the FIRST poll after it
//  4. acknowledged (the user looked at it)                  idle
func TestAcceptance2473_SmallWorkflowLifecycle(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_PROFILE", "")

	running := readAcceptanceFixture(t, "..", "..", "internal", "tmux", "testdata", "background_work", "workflow-running.txt")
	finished := readAcceptanceFixture(t, "..", "..", "internal", "tmux", "testdata", "background_work", "workflow-finished.txt")
	transcript := strings.Split(strings.TrimRight(readAcceptanceFixture(t, "..", "..", "internal", "session", "testdata", "background_work", "probe-workflow-agent-bash.jsonl"), "\n"), "\n")

	// Frames derived from the live running capture: the launching turn while
	// it is still busy (live spinner, no workflow row yet), and the row at
	// other progress points.
	launching := running[:strings.Index(running, "⏺ Workflow(")] +
		"⏺ Workflow(Two parallel agents each run sleep 60 via Bash and return done)\n\n" +
		"✢ Recombobulating… (2s · ↓ 168 tokens · thinking)\n" +
		running[strings.Index(running, "\n\n───"):strings.Index(running, "\n\n  ◯ probe-two-agents")]
	launching = strings.Replace(launching, "⏵⏵ bypass permissions on · 1 shell, 1 monitor · ← for agents", "⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents", 1)
	row := "◯ probe-two-agents  ▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱▱▱  1/2 · 22s · ↓ 73.3k tokens"
	if !strings.Contains(running, row) {
		t.Fatalf("fixture drifted: the running capture lacks %q", row)
	}
	atStep := func(bar, progress string) string {
		return strings.Replace(running, row, "◯ probe-two-agents  "+bar+"  "+progress+" · ↓ 73.3k tokens", 1)
	}

	pane := newAcceptancePane(t, home)
	pane.show(t, launching)
	inst := session.NewInstanceWithTool("acceptance-2473", home, "claude")
	ts := inst.GetTmuxSession()
	if err := ts.Start(pane.command); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	t.Cleanup(func() { _ = ts.Kill() })
	// The pane is a shell redrawing captured Claude frames; tell the tmux
	// layer whose renderings it reads (Start records the pane command).
	inst.Command = "claude"
	ts.Command = "claude"
	time.Sleep(2 * time.Second) // past UpdateStatus's tmux grace window
	pane.waitFor(t, ts, "Recombobulating")

	storage, err := session.NewStorageWithProfile("_test-acceptance-2473")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	type want struct {
		status, substate string
		step, steps      int // checked when work is expected
		work             bool
	}
	poll := func(phase string, w want) {
		t.Helper()
		check := func(surface string, fields map[string]interface{}) {
			t.Helper()
			if fields["status"] != w.status || fields["substate"] != w.substate {
				t.Fatalf("%s, %s: status/substate = %v/%v, want %s/%s (fields %v)", phase, surface, fields["status"], fields["substate"], w.status, w.substate, fields)
			}
			raw, present := fields["background_work"]
			if !w.work {
				if present && raw != nil {
					t.Fatalf("%s, %s: background_work = %v, want it omitted", phase, surface, raw)
				}
				return
			}
			b, _ := json.Marshal(raw)
			var bw map[string]interface{}
			_ = json.Unmarshal(b, &bw)
			if bw["kind"] != tmux.BackgroundKindWorkflow || bw["task"] != "probe-two-agents" ||
				bw["step"] != float64(w.step) || bw["steps"] != float64(w.steps) {
				t.Fatalf("%s, %s: background_work = %s, want workflow probe-two-agents %d/%d", phase, surface, b, w.step, w.steps)
			}
			wantDetail := fmt.Sprintf("workflow probe-two-agents %d/%d", w.step, w.steps)
			if d, _ := fields["substate_detail"].(string); !strings.HasPrefix(d, wantDetail) {
				t.Fatalf("%s, %s: substate_detail = %q, want prefix %q", phase, surface, d, wantDetail)
			}
		}
		// The long-lived process (TUI, web, notify daemon): `session show`'s
		// status pass, then `list --json` on the same instance.
		check("session show --json", sessionShowStatusFields(inst))
		check("list --json", acceptanceListRow(t, inst))
		// A fresh CLI process: the row the long-lived process persisted,
		// reloaded, with no in-memory state of its own.
		check("session show --json (fresh process)", sessionShowStatusFields(acceptanceReload(t, storage, inst)))
		check("list --json (fresh process)", acceptanceListRow(t, acceptanceReload(t, storage, inst)))
	}

	// 1. Started: UserPromptSubmit fired, the launching turn is busy.
	acceptanceHook(t, inst.ID, "running", "UserPromptSubmit")
	acceptanceTranscript(t, inst, transcript[:5], 1)
	poll("started", want{status: "running", substate: "running"})

	// 2. The launching turn ended: Claude's Stop hook says waiting, but the
	// workflow runs. Running + background-work on every poll, with the task
	// and its n/m, while its two agents work and report into the queue.
	acceptanceHook(t, inst.ID, "waiting", "Stop")
	steps := []struct {
		frame       string
		lines, step int
	}{
		{atStep("▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱", "0/2 · 3s"), 10, 0},
		{running, 10, 1}, // the live capture: 1/2 · 22s
		{atStep("▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱▱▱", "1/2 · 1m05s"), 14, 1}, // the agents' own notifications arrived
		{atStep("▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱▱▱", "1/2 · 1m40s"), 14, 1},
	}
	for i, s := range steps {
		pane.show(t, s.frame)
		pane.waitFor(t, ts, fmt.Sprintf("%d/2 ·", s.step))
		acceptanceTranscript(t, inst, transcript[:s.lines], 10+i)
		// Past the pane-probe cache, as the daemon's real cadence is.
		time.Sleep(tmuxBackgroundProbeTTL)
		poll(fmt.Sprintf("running poll %d", i+1), want{status: "running", substate: "background-work", work: true, step: s.step, steps: 2})
	}

	// 3. The workflow's terminal task notification arrives and Claude runs a
	// turn on it. The hook says running for that turn (recorded live:
	// logs/pr6-live-run-r3.log, 13:56:08), the pane shows the live spinner:
	// running, and no longer background-work.
	notifyBusy := finished[:strings.Index(finished, "⏺ The probe-two-agents workflow finished")] +
		"✢ Recombobulating… (3s · ↓ 210 tokens · thinking)" +
		finished[strings.Index(finished, "\n\n───"):]
	pane.show(t, notifyBusy)
	pane.waitFor(t, ts, "210 tokens")
	acceptanceTranscript(t, inst, transcript[:17], 25)
	acceptanceHook(t, inst.ID, "running", "UserPromptSubmit")
	poll("notification turn", want{status: "running", substate: "running"})

	// 4. The summary turn ends (Stop again). The first poll after it is
	// waiting: no sleep past any cache here.
	pane.show(t, finished)
	pane.waitFor(t, ts, "Cooked for 1m 55s")
	acceptanceTranscript(t, inst, transcript[:20], 30)
	acceptanceHook(t, inst.ID, "waiting", "Stop")
	poll("first poll after the workflow finished", want{status: "waiting", substate: "idle-at-empty-prompt"})

	// 5. The user looks at it (attach / the TUI acknowledgement): idle, on
	// the long-lived instance and from a fresh process alike.
	ts.Acknowledge()
	for _, surface := range []struct {
		name   string
		fields map[string]interface{}
	}{
		{"session show --json", sessionShowStatusFields(inst)},
		{"list --json", acceptanceListRow(t, inst)},
		{"session show --json (fresh process)", sessionShowStatusFields(acceptanceReload(t, storage, inst))},
		{"list --json (fresh process)", acceptanceListRow(t, acceptanceReload(t, storage, inst))},
	} {
		if surface.fields["status"] != "idle" {
			t.Fatalf("acknowledged, %s: status = %v, want idle (fields %v)", surface.name, surface.fields["status"], surface.fields)
		}
		if _, present := surface.fields["background_work"]; present && surface.fields["background_work"] != nil {
			t.Fatalf("acknowledged, %s: background_work present: %v", surface.name, surface.fields["background_work"])
		}
	}
}

// tmuxBackgroundProbeTTL is a little past tmux's background-work probe cache
// (bgWorkCacheTTL, 3s): the notify daemon polls every 1 to 3 s, so a frame
// that changed is seen by the next poll at the latest.
const tmuxBackgroundProbeTTL = 3200 * time.Millisecond

func readAcceptanceFixture(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// acceptancePane is a pane that redraws whatever frame file it is given, the
// way Claude Code repaints its screen.
type acceptancePane struct {
	frame   string
	command string
}

func newAcceptancePane(t *testing.T, dir string) *acceptancePane {
	t.Helper()
	p := &acceptancePane{frame: filepath.Join(dir, "frame.txt")}
	script := filepath.Join(dir, "redraw.sh")
	body := `#!/bin/sh
last=""
while :; do
  cur=$(cksum < "$1" 2>/dev/null)
  if [ "$cur" != "$last" ]; then
    printf '\033[H\033[2J'
    cat "$1"
    last="$cur"
  fi
  sleep 0.1
done
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	p.command = fmt.Sprintf("sh %q %q", script, p.frame)
	return p
}

func (p *acceptancePane) show(t *testing.T, frame string) {
	t.Helper()
	tmp := p.frame + ".tmp"
	if err := os.WriteFile(tmp, []byte(frame), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p.frame); err != nil {
		t.Fatal(err)
	}
}

// waitFor blocks until the pane shows marker, then lets the capture cache
// (500 ms) expire so the next status pass reads the new frame.
func (p *acceptancePane) waitFor(t *testing.T, ts *tmux.Session, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		content, err := ts.CapturePaneFresh()
		if err == nil && strings.Contains(content, marker) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane never showed %q; last capture:\n%s", marker, content)
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(600 * time.Millisecond)
}

// acceptanceHook writes the hook file Claude's hooks write for the session
// (sess-610 is the Claude session id the transcript is filed under).
func acceptanceHook(t *testing.T, instanceID, status, event string) {
	t.Helper()
	dir := session.GetHooksDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"status":%q,"session_id":"sess-610","event":%q,"ts":%d}`, status, event, time.Now().Unix())
	if err := os.WriteFile(filepath.Join(dir, instanceID+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

var acceptanceTimestampRe = regexp.MustCompile(`"timestamp": ?"[^"]+"`)

// acceptanceTranscript writes lines as the session's Claude transcript,
// stamped now (the records are as fresh as the run), with an mtime that
// moves forward on every write so the transcript cache sees the change.
func acceptanceTranscript(t *testing.T, inst *session.Instance, lines []string, seq int) {
	t.Helper()
	stamp := `"timestamp": "` + time.Now().UTC().Format(time.RFC3339Nano) + `"`
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = acceptanceTimestampRe.ReplaceAllString(l, stamp)
	}
	resolved := inst.ProjectPath
	if r, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = r
	}
	dir := filepath.Join(session.GetClaudeConfigDir(), "projects", session.ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sess-610.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(time.Duration(seq) * time.Second)
	_ = os.Chtimes(path, mt, mt)
}

// acceptanceListRow is the instance's row in `list --json`.
func acceptanceListRow(t *testing.T, inst *session.Instance) map[string]interface{} {
	t.Helper()
	session.RefreshInstancesForCLIStatus([]*session.Instance{inst})
	out, err := buildListJSON("_test", []*session.Instance{inst})
	if err != nil {
		t.Fatalf("buildListJSON: %v", err)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(out, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("list --json: %v (%d rows)\n%s", err, len(rows), out)
	}
	return rows[0]
}

// acceptanceReload persists inst as the long-lived process leaves it and
// returns the row a fresh CLI process loads.
func acceptanceReload(t *testing.T, storage *session.Storage, inst *session.Instance) *session.Instance {
	t.Helper()
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil || len(instances) != 1 {
		t.Fatalf("load: %v (%d instances)", err, len(instances))
	}
	return instances[0]
}
