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

// End-to-end restart tests for #2792: the real binary, a real (isolated) tmux
// server, a long-running notify daemon, and the Claude-like fake from
// delivery_e2e_test.go behind a `claude` command that also does what a
// restart needs from Claude: it resumes (--resume), fires SessionStart once it
// is up and SessionEnd when the restart kills it.
//
// A restart of a child idle at its prompt is quiet: the parent gets at most
// one info line, never an urgent record, an error or its old reply again. A
// restart in the middle of a turn still tells the parent the child stopped.
// Each case runs through the hook path (Claude's own hooks) and through the
// snapshot path (no hooks; the daemon sees the pane busy, then idle). Every
// case has its own tmux server and state, so they run in parallel.

// restartFakeClaude is deliveryFakeClaude plus --resume and the session
// hooks, which it skips when .fake-nohooks exists in the project.
const restartFakeClaude = `#!/bin/sh
sid=nosid
while [ $# -gt 0 ]; do
  case $1 in --session-id|--resume) sid=$2; shift 2 ;; *) shift ;; esac
done
here=$(cd "$(dirname "$0")" && pwd -P)
cwd=$(pwd -P)
proj="$HOME/.claude/projects/$(printf %s "$cwd" | sed 's/[^a-zA-Z0-9-]/-/g')"
mkdir -p "$proj" "$cwd/.fake"
t="$proj/$sid.jsonl"
: >> "$t"
phase=$(cat "$cwd/.fake-phase" 2>/dev/null || echo idle)
printf '{"id":"%s-%s","phase":"%s"}' "$phase" "$$" "$phase" > "$t.input-control.json"
printf '{"%s":"%s"}' "$cwd" "$t" > "$cwd/.fake/fake-running.json"
cp "$here/claude_like_fake.py" "$cwd/.fake/claude"
printf %s "$t" > "$cwd/.fake/transcript"
hook() {
  [ -e "$cwd/.fake-nohooks" ] && return 0
  printf '{"hook_event_name":"%s","session_id":"%s","cwd":"%s"}' "$1" "$sid" "$cwd" | "$here/agent-deck-bin" -p __PROFILE__ hook-handler >/dev/null 2>&1
  printf '%s\n' "$1" >> "$cwd/.fake/hooks-fired"
}
trap 'hook SessionEnd; exit 0' HUP TERM INT
(sleep 4; hook SessionStart) &
python3 -I "$cwd/.fake/claude"
`

func newRestartEnv(t *testing.T) *deliveryEnv {
	t.Helper()
	e := newDeliveryEnv(t)
	bin := filepath.Join(e.root, "fakebin")
	if err := os.Symlink(e.bin, filepath.Join(bin, "agent-deck-bin")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(bin, "claude"), strings.ReplaceAll(restartFakeClaude, "__PROFILE__", deliveryE2EProfile), 0o700)
	daemon := exec.Command(e.bin, "-p", deliveryE2EProfile, "notify-daemon")
	daemon.Env = e.env
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill(); _, _ = daemon.Process.Wait() })
	return e
}

// control switches the fake's phase without a hook (the snapshot path).
func (f *fakeTarget) control(t *testing.T, phase string) {
	t.Helper()
	f.phaseN++
	writeFile(t, f.transcript+".input-control.json", fmt.Sprintf(`{"id":"%s-c%d","phase":%q}`, phase, f.phaseN, phase), 0o600)
}

// waitRecords polls the parent's inbox until ok holds for the child's records.
func (e *deliveryEnv) waitRecords(parent, child *fakeTarget, within time.Duration, ok func([]map[string]any) bool) []map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(within)
	for {
		recs := e.pendingFrom(parent.id, child.id)
		if ok(recs) || time.Now().After(deadline) {
			return recs
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// restartChild restarts the child the way a person or the idle refresh does,
// through the snapshot path (booting, then idle, no hooks) or the hook path.
func (e *deliveryEnv) restartChild(child *fakeTarget, viaHooks bool) {
	e.t.Helper()
	if viaHooks {
		writeFile(e.t, filepath.Join(child.project, ".fake-phase"), "idle", 0o600)
	} else {
		writeFile(e.t, filepath.Join(child.project, ".fake-nohooks"), "", 0o600)
		writeFile(e.t, filepath.Join(child.project, ".fake-phase"), "booting", 0o600)
	}
	e.mustRun("session", "restart", "--force", child.id)
	if viaHooks {
		e.waitPane(child, "child back at its prompt", func(p string) bool { return strings.Contains(p, "All four steps ran.") })
		issue2530WaitFor(e.t, 10*time.Second, "SessionEnd and SessionStart from the restart", func() bool {
			b, _ := os.ReadFile(filepath.Join(child.project, ".fake", "hooks-fired"))
			return strings.Contains(string(b), "SessionEnd") && strings.Contains(string(b), "SessionStart")
		})
		return
	}
	e.waitPane(child, "child booting", func(p string) bool { return strings.Contains(p, "Thundering") })
	time.Sleep(4 * time.Second) // the daemon sees it running
	child.control(e.t, "idle")
	e.waitPane(child, "child back at its prompt", func(p string) bool { return strings.Contains(p, "All four steps ran.") })
}

// toolUse appends the tool call a turn makes after its interim text.
func (f *fakeTarget) toolUse(t *testing.T) {
	t.Helper()
	rec, _ := json.Marshal(map[string]any{
		"type": "assistant", "uuid": fmt.Sprintf("tool-%d", time.Now().UnixNano()),
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "sessionId": f.claudeSID,
		"message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "toolu_e2e", "name": "Bash", "input": map[string]string{"command": "sleep 600"}}}},
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

func recordText(r map[string]any) string { s, _ := r["text"].(string); return s }

func TestRestartE2E_IdleRestartIsQuiet(t *testing.T) {
	t.Parallel()
	for _, viaHooks := range []bool{true, false} {
		t.Run(map[bool]string{true: "hook path", false: "snapshot path"}[viaHooks], func(t *testing.T) {
			t.Parallel()
			e := newRestartEnv(t)
			parent := e.startFake("parent", "idle", "")
			child := e.startFake("child", "busy", parent.id)
			e.setPhase(child, "busy")
			time.Sleep(3 * time.Second)
			answer := deliveryToken("CHILD_REPLY")
			child.reply(t, answer+" the task is done")
			e.setPhase(child, "idle")
			recs := e.waitRecords(parent, child, 20*time.Second, func(r []map[string]any) bool { return len(r) >= 1 })
			if len(recs) != 1 || !strings.Contains(recordText(recs[0]), answer) {
				t.Fatalf("precondition: the real turn reaches the parent once, got %v", recs)
			}
			e.drainedFrom(parent.id, child.id)

			e.restartChild(child, viaHooks)
			time.Sleep(8 * time.Second) // several daemon passes after the boot
			recs = e.pendingFrom(parent.id, child.id)
			if len(recs) != 1 {
				t.Fatalf("an idle restart gives the parent exactly one info line, got %d: %v", len(recs), recs)
			}
			for _, r := range recs {
				if r["tier"] != "info" || r["to_status"] == "error" || strings.Contains(recordText(r), answer) ||
					!strings.Contains(recordText(r), "conversation resumed, no new turn") {
					t.Fatalf("an idle restart must reach the parent only as the info line, got %v", r)
				}
			}
		})
	}
}

func TestRestartE2E_MidTurnRestartStillNotifies(t *testing.T) {
	t.Parallel()
	for _, viaHooks := range []bool{true, false} {
		t.Run(map[bool]string{true: "hook path", false: "snapshot path"}[viaHooks], func(t *testing.T) {
			t.Parallel()
			e := newRestartEnv(t)
			parent := e.startFake("parent", "idle", "")
			child := e.startFake("child", "busy", parent.id)
			e.setPhase(child, "busy")
			child.reply(t, "Running the migration now.")
			child.toolUse(t)
			time.Sleep(3 * time.Second) // the daemon sees the child working
			e.drainedFrom(parent.id, child.id)

			e.restartChild(child, viaHooks)
			recs := e.waitRecords(parent, child, 20*time.Second, func(r []map[string]any) bool {
				for _, rec := range r {
					if !strings.Contains(recordText(rec), "no new turn") {
						return true
					}
				}
				return false
			})
			stopped := false
			for _, r := range recs {
				stopped = stopped || !strings.Contains(recordText(r), "no new turn")
			}
			if !stopped {
				t.Fatalf("a restart in the middle of a turn must tell the parent the child stopped, got %v", recs)
			}
		})
	}
}
