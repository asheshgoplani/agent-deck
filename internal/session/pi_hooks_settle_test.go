package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The v3 extension reports a run's text once, when pi says the run has
// settled. These tests drive the real shipped source under node with a
// recording stand-in for `agent-deck hook-handler`, feeding it the event
// sequence pi fires for one run, and read back what it sent.

type piStep struct {
	Event   string         `json:"event"`
	Payload map[string]any `json:"payload,omitempty"`
}

// piSent is what the extension handed to hook-handler during one step.
type piSent struct {
	Event    string
	Payloads []map[string]any
}

type piHarness struct {
	t       *testing.T
	node    string
	extPath string
	drvPath string
	binDir  string
	recPath string
}

func newPiHarness(t *testing.T) *piHarness {
	t.Helper()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available in PATH, skipping extension test")
	}
	dir := t.TempDir()
	h := &piHarness{t: t, node: nodePath, extPath: filepath.Join(dir, "ext.ts"), drvPath: filepath.Join(dir, "drive.mjs"),
		binDir: filepath.Join(dir, "bin"), recPath: filepath.Join(dir, "sent.jsonl")}
	if err := os.WriteFile(h.extPath, []byte(PiHookExtensionSource()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(h.binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\ncat >> \"$PI_TEST_REC\"\necho >> \"$PI_TEST_REC\"\n"
	if err := os.WriteFile(filepath.Join(h.binDir, "agent-deck"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	// One process and one context for the whole sequence, as pi runs it. A
	// marker line separates what each event caused.
	driver := `import ext from "` + h.extPath + `";
import { appendFileSync, readFileSync } from "node:fs";
const handlers = {};
ext({ on(ev, h) { handlers[ev] = h; } });
const ctx = { cwd: process.cwd(), sessionManager: { getSessionId() { return "pi-sess-1"; }, getSessionFile() { return "/tmp/pi-sess-1.jsonl"; } } };
for (const step of JSON.parse(readFileSync(process.argv[2], "utf8"))) {
  if (!handlers[step.event]) throw new Error("extension has no " + step.event + " handler");
  await handlers[step.event](step.payload ? { type: step.event, ...step.payload } : undefined, ctx);
  appendFileSync(process.env.PI_TEST_REC, JSON.stringify({ marker: step.event }) + "\n");
}
`
	if err := os.WriteFile(h.drvPath, []byte(driver), 0o644); err != nil {
		t.Fatal(err)
	}
	return h
}

// run feeds the steps to the extension and returns what each one sent.
func (h *piHarness) run(steps []piStep) []piSent {
	h.t.Helper()
	_ = os.Remove(h.recPath)
	script := filepath.Join(h.t.TempDir(), "steps.json")
	data, _ := json.Marshal(steps)
	if err := os.WriteFile(script, data, 0o644); err != nil {
		h.t.Fatal(err)
	}
	cmd := exec.Command(h.node, "--experimental-strip-types", "--no-warnings", h.drvPath, script)
	cmd.Env = append(os.Environ(),
		"AGENTDECK_INSTANCE_ID=pi-settle-test",
		"PI_TEST_REC="+h.recPath,
		"PATH="+h.binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		h.t.Fatalf("driver failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(h.recPath)
	if err != nil {
		h.t.Fatalf("read recording: %v", err)
	}
	var sent []piSent
	var pending []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			h.t.Fatalf("unreadable recording line %q: %v", line, err)
		}
		if ev, ok := m["marker"].(string); ok {
			sent = append(sent, piSent{Event: ev, Payloads: pending})
			pending = nil
			continue
		}
		pending = append(pending, m)
	}
	return sent
}

func piTurnEndPayload(text, entryID, outcome string) map[string]any {
	content := []any{map[string]any{"type": "toolCall", "id": "call-1", "name": "bash", "arguments": map[string]any{}}}
	if text != "" {
		content = []any{map[string]any{"type": "text", "text": text}}
	}
	return map[string]any{
		"turnIndex": 0, "message": map[string]any{"role": "assistant", "content": content, "stopReason": "stop"},
		"toolResults": []any{}, "messageEntryId": entryID, "toolResultEntryIds": []any{}, "outcome": outcome,
	}
}

// withStopReason sets the stop reason pi put on the turn's assistant message.
func withStopReason(turnEnd map[string]any, reason string) map[string]any {
	turnEnd["message"].(map[string]any)["stopReason"] = reason
	return turnEnd
}

func piRun(prompt string, turnEnd map[string]any) []piStep {
	return []piStep{
		{Event: "input", Payload: map[string]any{"text": prompt, "source": "interactive"}},
		{Event: "agent_start", Payload: map[string]any{}},
		{Event: "turn_start", Payload: map[string]any{"turnIndex": 0, "timestamp": 1791072000000}},
		{Event: "turn_end", Payload: turnEnd},
		{Event: "agent_settled", Payload: map[string]any{"aborted": false}},
	}
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// settlePayloads returns what the agent_settled step sent, after checking
// nothing before it carried the run's text.
func settlePayloads(t *testing.T, sent []piSent) []map[string]any {
	t.Helper()
	var settle []map[string]any
	for _, s := range sent {
		for _, p := range s.Payloads {
			if s.Event != "agent_settled" && str(p, "last_assistant_message") != "" {
				t.Fatalf("%s sent run text before the run settled: %v", s.Event, p)
			}
		}
		if s.Event == "agent_settled" {
			settle = s.Payloads
		}
	}
	return settle
}

func TestPiHookExtension_SettleSendsOneTextPayload(t *testing.T) {
	h := newPiHarness(t)
	const text = "Built and tested.\n===AGENTDECK_DONE=== status=ok summary=matrix"
	const prompt = "[agent-deck from:conductor-1] build it"
	sent := h.run(piRun(prompt, piTurnEndPayload(text, "entry-1", "completed")))

	settle := settlePayloads(t, sent)
	if len(settle) != 1 {
		t.Fatalf("agent_settled must invoke hook-handler exactly once, got %d: %v", len(settle), settle)
	}
	p := settle[0]
	if str(p, "session_id") != "pi-sess-1" || str(p, "turn_id") != "entry-1" || str(p, "last_assistant_message") != text ||
		str(p, "prompt") != prompt || str(p, "source") != "pi" {
		t.Fatalf("settle payload: %v", p)
	}
}

func TestPiHookExtension_SettleCarriesTheLastInputAsPrompt(t *testing.T) {
	h := newPiHarness(t)
	steps := piRun("first ask", piTurnEndPayload("Answer.", "entry-1", "completed"))
	// A message queued while the run was busy arrives as another input.
	steps = append(steps[:4], append([]piStep{{Event: "input", Payload: map[string]any{"text": "[agent-deck from:conductor-1] also this", "source": "extension"}}}, steps[4:]...)...)
	settle := settlePayloads(t, h.run(steps))
	if len(settle) != 1 || str(settle[0], "prompt") != "[agent-deck from:conductor-1] also this" {
		t.Fatalf("settle must carry the last input text: %v", settle)
	}
}

func TestPiHookExtension_NoTextSettleForToolOnlyAndNonCompletedRuns(t *testing.T) {
	cases := []struct {
		name    string
		turnEnd map[string]any
		settled map[string]any
	}{
		{"tool_only", piTurnEndPayload("", "entry-1", "completed"), map[string]any{"aborted": false}},
		{"aborted", withStopReason(piTurnEndPayload("Partial answer.", "entry-1", "aborted"), "aborted"), map[string]any{"aborted": false}},
		{"error", withStopReason(piTurnEndPayload("Partial answer.", "entry-1", "error"), "error"), map[string]any{"aborted": false}},
		// Escape during tool execution still ends the turn as completed, with
		// the stop reason of a turn that asked for tools.
		{"settled_aborted", withStopReason(piTurnEndPayload("Let me run the tests.", "entry-1", "completed"), "toolUse"), map[string]any{"aborted": true}},
		// Without an aborted field, the stop reason alone shows the run never
		// produced a final answer.
		{"tool_use_last_turn_without_aborted", withStopReason(piTurnEndPayload("Let me run the tests.", "entry-1", "completed"), "toolUse"), map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPiHarness(t)
			steps := piRun("do it", tc.turnEnd)
			steps[len(steps)-1].Payload = tc.settled
			settle := settlePayloads(t, h.run(steps))
			if len(settle) != 1 {
				t.Fatalf("a no-text run still settles once, got %d: %v", len(settle), settle)
			}
			p := settle[0]
			if str(p, "last_assistant_message") != "" {
				t.Fatalf("no-text settle carried text: %v", p)
			}
			if str(p, "session_id") != "pi-sess-1" || str(p, "turn_id") != "entry-1" || str(p, "source") != "pi" {
				t.Fatalf("no-text settle identity: %v", p)
			}
		})
	}
}

func TestPiHookExtension_AgentStartResetsTheRunsState(t *testing.T) {
	h := newPiHarness(t)
	steps := piRun("first ask", piTurnEndPayload("First answer.", "entry-1", "completed"))
	// The second run settles without a turn_end (pi skips it when it cannot
	// resolve the entry id), so nothing of the first run may carry over.
	steps = append(steps,
		piStep{Event: "input", Payload: map[string]any{"text": "second ask", "source": "interactive"}},
		piStep{Event: "agent_start", Payload: map[string]any{}},
		piStep{Event: "agent_settled", Payload: map[string]any{"aborted": false}},
	)
	sent := h.run(steps)
	var settles [][]map[string]any
	for _, s := range sent {
		if s.Event == "agent_settled" {
			settles = append(settles, s.Payloads)
		}
	}
	if len(settles) != 2 || len(settles[0]) != 1 {
		t.Fatalf("settles: %v", settles)
	}
	for _, p := range settles[1] {
		if str(p, "last_assistant_message") != "" || str(p, "turn_id") == "entry-1" || strings.Contains(str(p, "prompt"), "first ask") {
			t.Fatalf("second run reused the first run's state: %v", p)
		}
	}
}

func TestPiHookExtension_OnlyTheLastTurnOfARunIsReported(t *testing.T) {
	h := newPiHarness(t)
	steps := []piStep{
		{Event: "input", Payload: map[string]any{"text": "do it", "source": "interactive"}},
		{Event: "agent_start", Payload: map[string]any{}},
		{Event: "turn_start", Payload: map[string]any{"turnIndex": 0}},
		{Event: "turn_end", Payload: piTurnEndPayload("Reading the file.", "entry-1", "completed")},
		{Event: "turn_start", Payload: map[string]any{"turnIndex": 1}},
		{Event: "turn_end", Payload: piTurnEndPayload("All done.", "entry-2", "completed")},
		{Event: "agent_settled", Payload: map[string]any{"aborted": false}},
	}
	settle := settlePayloads(t, h.run(steps))
	if len(settle) != 1 || str(settle[0], "turn_id") != "entry-2" || str(settle[0], "last_assistant_message") != "All done." {
		t.Fatalf("settle must report the run's last turn: %v", settle)
	}
}

func TestPiHookExtension_TailCapsLongText(t *testing.T) {
	h := newPiHarness(t)
	const sentinel = "\n===AGENTDECK_DONE=== status=ok summary=long"
	text := strings.Repeat("0123456789abcdef", 4096) + sentinel // 64 KiB then the sentinel
	settle := settlePayloads(t, h.run(piRun("[agent-deck from:c] go", piTurnEndPayload(text, "entry-1", "completed"))))
	if len(settle) != 1 {
		t.Fatalf("settle payloads: %d", len(settle))
	}
	got := str(settle[0], "last_assistant_message")
	if len(got) > 16<<10 || len(got) < 8<<10 {
		t.Fatalf("text must be capped to 16 KiB, kept %d bytes", len(got))
	}
	if !strings.HasSuffix(got, sentinel) {
		t.Fatalf("the trailing sentinel must survive the cap; tail is %q", got[len(got)-80:])
	}
	if !strings.HasSuffix(text, got) {
		t.Fatal("the capped text must be a suffix of the original")
	}
	raw, _ := json.Marshal(settle[0])
	if len(raw) >= 1<<20 {
		t.Fatalf("payload is %d bytes; hook-handler reads at most 1 MiB", len(raw))
	}
}

// pi calls handlers with whatever it has; an extension handler must never
// throw on a missing event or context.
func TestPiHookExtension_HandlersTolerateNoArguments(t *testing.T) {
	h := newPiHarness(t)
	var steps []piStep
	for _, ev := range []string{"session_start", "agent_start", "input", "turn_start", "turn_end", "agent_settled", "session_shutdown"} {
		steps = append(steps, piStep{Event: ev})
	}
	h.run(steps)
}

// The matrix driver keeps one callback per event name, so a second pi.on for
// the same event would never run there.
func TestPiHookExtension_RegistersEachEventOnce(t *testing.T) {
	src := PiHookExtensionSource()
	for _, ev := range []string{"session_start", "agent_start", "input", "turn_start", "turn_end", "agent_settled", "session_shutdown"} {
		if n := strings.Count(src, `pi.on("`+ev+`"`); n != 1 {
			t.Errorf("pi.on(%q) registered %d times, want exactly 1", ev, n)
		}
	}
}
