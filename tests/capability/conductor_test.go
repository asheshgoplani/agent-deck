//go:build capability_e2e

package capability

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runHook invokes the real `agent-deck hook-handler` exactly as Claude Code
// does: AGENTDECK_INSTANCE_ID in the env, the JSON event on stdin. It returns
// nothing (the handler always exits 0); the effect is the hook status file.
func (c *capSandbox) runHook(t *testing.T, instanceID, payload string) {
	t.Helper()
	cmd := exec.Command(c.BinPath, "hook-handler")
	cmd.Env = append(c.Env(), "AGENTDECK_INSTANCE_ID="+instanceID)
	cmd.Dir = c.Home
	cmd.Stdin = strings.NewReader(payload)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook-handler: %v\n%s", err, out)
	}
}

// hookStatus is the subset of the persisted hook status file the conductor
// backbone reads to decide whether a child has finished.
type hookStatus struct {
	Status      string `json:"status"`
	Event       string `json:"event"`
	DoneStatus  string `json:"done_status"`
	DoneSummary string `json:"done_summary"`
}

func (c *capSandbox) readHookStatus(t *testing.T, instanceID string) hookStatus {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.Home, ".agent-deck", "hooks", instanceID+".json"))
	if err != nil {
		t.Fatalf("read hook status file: %v", err)
	}
	var hs hookStatus
	if err := json.Unmarshal(raw, &hs); err != nil {
		t.Fatalf("parse hook status: %v\nraw: %s", err, raw)
	}
	return hs
}

// writeDoneTranscript writes a Claude-style transcript whose final assistant
// turn carries the worker completion sentinel, under HOME/.claude (the only
// location the Stop-hook handler will read, by its path-containment guard).
func (c *capSandbox) writeDoneTranscript(t *testing.T, body string) string {
	t.Helper()
	dir := filepath.Join(c.Home, ".claude", "projects", "cap")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":` +
		mustJSON(t, body) + `}]}}`
	path := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// childIDFor adds a hook-bearing child (tool must be claude/codex/gemini so the
// daemon reads its hook status) linked to a stopped parent, and returns the
// child's full registry id (the daemon keys events on the id). The parent is a
// plain session that is never started: the finished/transition EVENT is
// generated and logged regardless of whether the parent pane is live (live
// tmux delivery is the unit-tested notifier seam).
//
// Tool choice matters: a gemini child is read for done signals but is NOT a
// transition candidate, so a finished-signal test gets a clean, uncontested
// [DONE] emission; a claude child IS a transition candidate, which the dedup
// test needs.
func (c *capSandbox) childIDFor(t *testing.T, parentTitle, childTitle, tool string) string {
	t.Helper()
	c.run(t, "add", "-c", "bash", "-t", parentTitle, c.WorkDir)
	c.run(t, "add", "-c", tool, "-t", childTitle, "--parent", parentTitle, c.WorkDir)
	row, ok := c.findByTitle(t, childTitle)
	if !ok {
		t.Fatalf("child %q not created.\nrows: %+v", childTitle, c.list(t))
	}
	return row.ID
}

// transitionLogLinesFor returns the transition-notifier.log lines that mention
// the given child id. Each delivered/attempted event appends one JSON line; a
// deduped event appends none.
func (c *capSandbox) transitionLogLinesFor(childID string) []string {
	raw, err := os.ReadFile(filepath.Join(c.Home, ".agent-deck", "logs", "transition-notifier.log"))
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.Contains(ln, childID) {
			out = append(out, ln)
		}
	}
	return out
}

// TestCapability_Conductor_FinishedSignal proves the issue #1186 completion
// backbone end to end through the binary: a worker prints the DONE sentinel,
// the real Stop-hook handler detects it from the transcript and persists a
// done outcome, and the notifier daemon turns that into a distinct "finished"
// event for the parent (the [DONE] signal a conductor consumes, not the
// ambiguous "waiting").
//
// Surfaces: CLI (hook-handler, notify-daemon) + Persistence (hook status file,
// transition-notifier.log) + Remote (parent-target resolution). Live tmux
// delivery of the [DONE] line into a real conductor pane is the unit-tested
// notifier seam (internal/session) and the real-agent path is Tier N.
func TestCapability_Conductor_FinishedSignal(t *testing.T) {
	c := newCapSandbox(t)
	// A gemini child is read for done signals but does not also emit a
	// transition candidate, so the finished event is the only thing on the
	// wire — no slot race with a co-emitted "waiting" transition.
	childID := c.childIDFor(t, "cap-parent", "cap-child", "gemini")

	// Failure mode first: an ordinary Stop with NO sentinel must leave the
	// done fields absent — the conductor must not mistake a mid-task pause for
	// completion.
	plainTranscript := c.writeDoneTranscript(t, "still working, no sentinel here")
	c.runHook(t, childID, stopPayload(plainTranscript))
	if hs := c.readHookStatus(t, childID); hs.DoneStatus != "" {
		t.Fatalf("ordinary Stop must not set done_status, got %q", hs.DoneStatus)
	}

	// Happy path: the worker prints the sentinel on its final turn.
	transcript := c.writeDoneTranscript(t,
		"all set.\n===AGENTDECK_DONE=== status=ok summary=capability wave2 shipped")
	c.runHook(t, childID, stopPayload(transcript))

	hs := c.readHookStatus(t, childID)
	if hs.DoneStatus != "ok" {
		t.Fatalf("Stop-hook should persist done_status=ok, got %q (full: %+v)", hs.DoneStatus, hs)
	}
	if !strings.Contains(hs.DoneSummary, "capability wave2 shipped") {
		t.Errorf("done_summary = %q, want it to carry the worker summary", hs.DoneSummary)
	}

	// The daemon turns the persisted sentinel into a finished event.
	c.run(t, "notify-daemon", "--once")
	lines := c.transitionLogLinesFor(childID)
	finished := ""
	for _, ln := range lines {
		if strings.Contains(ln, `"kind":"finished"`) {
			finished = ln
		}
	}
	if finished == "" {
		t.Fatalf("notify-daemon should emit a finished event for the child.\nlog lines: %v", lines)
	}
	if !strings.Contains(finished, `"done_status":"ok"`) {
		t.Errorf("finished event should carry done_status=ok:\n%s", finished)
	}

	// Display proof: the persisted hook status (the conductor's finished
	// signal) and the daemon's finished event, distilled to the fields that
	// matter — status ok and the summary.
	snapshot(t, "conductor-finished", "hook status file (what the Stop-hook persisted):\n"+
		mustPretty(t, c.readHookStatus(t, childID))+
		"\n\nfinished event emitted by notify-daemon (the [DONE] signal to the parent):\n"+
		finishedEventDigest(finished))
}

// TestCapability_Conductor_Dedup proves the issue #1187 dedup is durable across
// daemon polls: the same idle child re-observed on a second pass does NOT
// re-fire its transition event. The dedup ledger is persisted to disk, so two
// independent `notify-daemon --once` processes share it — exactly the case that
// regressed when the dedup key was clock-derived and moved every poll.
//
// Surfaces: CLI (hook-handler, notify-daemon) + Persistence (transition-notify
// state ledger). Drives a Stop transition (no sentinel) so the transition path
// — the one #1187 fixed — is the thing under test.
func TestCapability_Conductor_Dedup(t *testing.T) {
	c := newCapSandbox(t)
	// A claude child is a transition candidate, so a Stop produces the
	// running->waiting transition event that #1187's dedup governs.
	childID := c.childIDFor(t, "dd-parent", "dd-child", "claude")

	// A terminal Stop with no sentinel is a transition candidate (running ->
	// waiting), the event #1187 governs.
	transcript := c.writeDoneTranscript(t, "ordinary turn, back at the prompt")
	c.runHook(t, childID, stopPayload(transcript))

	// First poll: emits one transition event and persists the dedup record.
	c.run(t, "notify-daemon", "--once")
	first := c.transitionLogLinesFor(childID)
	if len(first) != 1 {
		t.Fatalf("first poll should emit exactly one transition event, got %d:\n%s",
			len(first), strings.Join(first, "\n"))
	}

	// Second poll, fresh process: the persisted ledger suppresses the re-fire.
	c.run(t, "notify-daemon", "--once")
	second := c.transitionLogLinesFor(childID)
	if len(second) != 1 {
		t.Fatalf("an idle child must not re-fire across polls (issue #1187 dedup); got %d lines:\n%s",
			len(second), strings.Join(second, "\n"))
	}

	// Display proof: the single event row that survived two polls, proving the
	// idle child fired once and was deduped on the second pass.
	snapshot(t, "conductor-dedup", "two `notify-daemon --once` passes over the same idle child\n"+
		"emitted exactly one transition event (the second was deduped):\n\n"+
		finishedEventDigest(second[0]))
}

// stopPayload builds the Stop hook JSON Claude Code sends, pointing at the
// given transcript.
func stopPayload(transcriptPath string) string {
	return `{"hook_event_name":"Stop","session_id":"cap-sid","transcript_path":` +
		jsonString(transcriptPath) + `}`
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func mustPretty(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// finishedEventDigest pulls the human-meaningful fields out of a raw notifier
// log line so the snapshot reads cleanly instead of dumping the full JSON with
// timestamps and internal delivery bookkeeping.
func finishedEventDigest(line string) string {
	var ev map[string]any
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return strings.TrimSpace(line)
	}
	keep := []string{"child_title", "child_session_id", "kind", "from_status", "to_status", "done_status", "done_summary"}
	var b strings.Builder
	for _, k := range keep {
		if v, ok := ev[k]; ok && v != "" && v != nil {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(strings.TrimSpace(toStr(v)))
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
