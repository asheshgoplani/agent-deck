package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// Fixture builders mirror Claude Code 2.1.288 transcript records as observed
// on 2026-10-03 in a child running background agents (issue #2469).

func fxUser(uuid, text string, extra map[string]any) string {
	rec := map[string]any{
		"type": "user", "uuid": uuid, "isSidechain": false,
		"message": map[string]any{"role": "user", "content": text},
	}
	for k, v := range extra {
		rec[k] = v
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

func fxTaskNotification(uuid string) string {
	return fxUser(uuid, "<task-notification>\n<task-id>abc</task-id>\n<status>completed</status>\n</task-notification>", map[string]any{
		"turnOrigin":   "task_notification",
		"origin":       map[string]any{"kind": "task-notification", "producer": "session-task"},
		"promptSource": "system",
	})
}

func fxHuman(uuid, text string) string {
	return fxUser(uuid, text, map[string]any{"turnOrigin": "human", "origin": map[string]any{"kind": "human"}, "promptSource": "typed"})
}

func fxAssistantText(uuid, text string) string {
	rec := map[string]any{
		"type": "assistant", "uuid": uuid, "isSidechain": false,
		"message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": text}}},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

func fxAssistantToolUse(uuid string) string {
	rec := map[string]any{
		"type": "assistant", "uuid": uuid, "isSidechain": false,
		"message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{}}}},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

func fxToolResult(uuid string) string {
	rec := map[string]any{
		"type": "user", "uuid": uuid, "isSidechain": false,
		"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": "ok"}}},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

func fxSidechain(uuid string) string {
	rec := map[string]any{
		"type": "assistant", "uuid": uuid, "isSidechain": true,
		"message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "subagent chatter"}}},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

func TestClassifyTranscriptTail_BackgroundTaskTurnIsInfo(t *testing.T) {
	lines := []string{
		fxHuman("u0", "do the board"),
		fxAssistantText("a0", "Starting the lanes."),
		fxTaskNotification("u1"),
		fxAssistantToolUse("a1"),
		fxToolResult("u2"),
		fxSidechain("s1"),
		fxAssistantText("a2", "Lane C merged; two verifiers still running."),
	}
	f := classifyTranscriptTail(lines)
	if f.Pending {
		t.Fatal("not pending: the assistant record flushed")
	}
	if f.Trigger != TurnTriggerTask {
		t.Fatalf("trigger = %q, want task", f.Trigger)
	}
	if f.UUID != "a2" || f.Text != "Lane C merged; two verifiers still running." {
		t.Fatalf("wrong turn text/uuid: %+v", f)
	}
	if f.HasDone || f.Question {
		t.Fatalf("no sentinel, no question: %+v", f)
	}
	if got := ClassifyTurnTier(f, "waiting", nil); got != TurnTierInfo {
		t.Fatalf("tier = %q, want info", got)
	}
}

func TestClassifyTranscriptTail_HumanTurnReplyIsInfo(t *testing.T) {
	lines := []string{fxHuman("u1", "status?"), fxAssistantText("a1", "All lanes green.")}
	f := classifyTranscriptTail(lines)
	if f.Trigger != TurnTriggerHuman {
		t.Fatalf("trigger = %q", f.Trigger)
	}
	// Conductor ruling 2026-10-03: a plain reply, even to the parent's own
	// prompt, is info. Only a sentinel, an error or a question is urgent.
	if got := ClassifyTurnTier(f, "waiting", nil); got != TurnTierInfo {
		t.Fatalf("tier = %q, want info", got)
	}
}

// The four replies that woke the conductor on 2026-10-03 although they were
// progress notes or acknowledgements, as fixtures: each is a reply to a send
// (tagged or plain) and must classify as info.
func TestClassifyTurnTier_ProgressRepliesToSendsAreInfo(t *testing.T) {
	cases := []struct {
		name, prompt, reply string
		tagged              bool
	}{
		{"macapp re-freeze report", "status of the freeze?", "Re-freeze report: p9 preview rebuilt after the layout fix, full window suite green (0 skips), the two accepted reds unchanged. Freeze is on; INSTALL.txt names the accepted failures.", true},
		{"ledger round 2 saved", "how is round 2 going?", "Round 2 report saved, waiting for Docker.", true},
		{"ledger relay fold", "proceed with the relay rule", "relay-rule fold committed, suites running.", false},
		{"macapp noted study", "FYI: the MonoCode study is in the shared folder", "Noted the MonoCode study; nothing changes for the freeze.", true},
	}
	for _, c := range cases {
		prompt := c.prompt
		if c.tagged {
			prompt = "[agent-deck from:conductor-1] " + prompt
		}
		f := classifyTranscriptTail([]string{fxHuman("u1", prompt), fxAssistantText("a1", c.reply)})
		if want := map[bool]string{true: TurnTriggerSend, false: TurnTriggerHuman}[c.tagged]; f.Trigger != want {
			t.Fatalf("%s: trigger = %q, want %q", c.name, f.Trigger, want)
		}
		if f.Question || f.HasDone {
			t.Fatalf("%s: neither a question nor a sentinel: %+v", c.name, f)
		}
		if got := ClassifyTurnTier(f, "waiting", nil); got != TurnTierInfo {
			t.Fatalf("%s: tier = %q, want info (a progress note must not wake the parent)", c.name, got)
		}
	}
	// The same reply with an explicit question, or a sentinel, or at error, is urgent.
	q := classifyTranscriptTail([]string{fxHuman("u1", "[agent-deck from:c] go"), fxAssistantText("a1", "Round 2 saved.\nNEED: may I merge?")})
	if ClassifyTurnTier(q, "waiting", nil) != TurnTierUrgent {
		t.Fatal("question must be urgent")
	}
	d := classifyTranscriptTail([]string{fxHuman("u1", "go"), fxAssistantText("a1", "Done.\n===AGENTDECK_DONE=== status=ok summary=x")})
	if ClassifyTurnTier(d, "waiting", nil) != TurnTierUrgent {
		t.Fatal("sentinel must be urgent")
	}
	e := classifyTranscriptTail([]string{fxHuman("u1", "go"), fxAssistantText("a1", "Login expired · Please run /login")})
	if ClassifyTurnTier(e, "error", nil) != TurnTierUrgent {
		t.Fatal("error status must be urgent")
	}
}

func TestClassifyTranscriptTail_QuestionAndSentinelAreUrgent(t *testing.T) {
	q := classifyTranscriptTail([]string{fxTaskNotification("u1"), fxAssistantText("a1", "Lane B needs a ruling.\nNEED: merge or hold?")})
	if !q.Question || ClassifyTurnTier(q, "waiting", nil) != TurnTierUrgent {
		t.Fatalf("question turn must be urgent: %+v", q)
	}
	d := classifyTranscriptTail([]string{fxTaskNotification("u1"), fxAssistantText("a1", "All done.\n===AGENTDECK_DONE=== status=ok summary=board at zero")})
	if !d.HasDone || d.Done.Status != "ok" || d.Done.Summary != "board at zero" {
		t.Fatalf("sentinel not parsed: %+v", d)
	}
	if ClassifyTurnTier(d, "waiting", nil) != TurnTierUrgent {
		t.Fatal("sentinel turn must be urgent")
	}
	// A background turn whose text merely contains a question mark mid-text
	// but ends with a statement is info.
	m := classifyTranscriptTail([]string{fxTaskNotification("u1"), fxAssistantText("a1", "Was it flaky? Re-ran, green now.")})
	if m.Question {
		t.Fatalf("mid-text ? is not a question: %+v", m)
	}
}

func TestClassifyTranscriptTail_PendingWhenReplyUnflushed(t *testing.T) {
	f := classifyTranscriptTail([]string{fxAssistantText("a0", "old"), fxTaskNotification("u1")})
	if !f.Pending {
		t.Fatalf("Stop outran the flush: want pending, got %+v", f)
	}
	// Only tool calls so far, no final text: still pending.
	f = classifyTranscriptTail([]string{fxAssistantText("a0", "old"), fxTaskNotification("u1"), fxAssistantToolUse("a1"), fxToolResult("u2")})
	if !f.Pending {
		t.Fatalf("tool-only tail: want pending, got %+v", f)
	}
}

func TestClassifyTurnTier_NoiseOnUnchangedTextAndClass(t *testing.T) {
	f := classifyTranscriptTail([]string{fxTaskNotification("u1"), fxAssistantText("a1", "same text")})
	prev := &TurnJournalEntry{Status: "waiting", TextHash: f.TextHash, Tier: TurnTierInfo}
	if got := ClassifyTurnTier(f, "waiting", prev); got != TurnTierNoise {
		t.Fatalf("hook re-fire: tier = %q, want noise", got)
	}
	if got := ClassifyTurnTier(f, "idle", prev); got != TurnTierNoise {
		t.Fatalf("waiting->idle flip with same text: tier = %q, want noise", got)
	}
	if got := ClassifyTurnTier(f, "error", prev); got != TurnTierUrgent {
		t.Fatalf("flip into error is never noise: tier = %q", got)
	}
	changed := classifyTranscriptTail([]string{fxTaskNotification("u1"), fxAssistantText("a2", "new text")})
	if got := ClassifyTurnTier(changed, "waiting", prev); got != TurnTierInfo {
		t.Fatalf("new text after a background turn: tier = %q, want info", got)
	}
	// A new sentinel on otherwise identical text is urgent, not noise.
	done := classifyTranscriptTail([]string{fxTaskNotification("u1"), fxAssistantText("a3", "same text\n===AGENTDECK_DONE=== status=ok summary=x")})
	prevSame := &TurnJournalEntry{Status: "waiting", TextHash: done.TextHash}
	if got := ClassifyTurnTier(done, "waiting", prevSame); got != TurnTierUrgent {
		t.Fatalf("new sentinel: tier = %q, want urgent", got)
	}
}

func TestClassifyTrigger_PrefixFallbacksAndEnvelope(t *testing.T) {
	cases := map[string]string{
		fxUser("u", "<task-notification>x</task-notification>", nil):        TurnTriggerTask,
		fxUser("u", "[INBOX] A child just committed a completion", nil):     TurnTriggerInbox,
		fxUser("u", "Stop hook feedback:\nChild session(s) completed", nil): TurnTriggerInbox,
		fxUser("u", "[HEARTBEAT] Check sessions", nil):                      TurnTriggerInbox,
		fxUser("u", "<system-reminder>ctx</system-reminder>", nil):          TurnTriggerSystem,
		fxUser("u", "anything", map[string]any{"isMeta": true}):             TurnTriggerSystem,
		fxUser("u", "[agent-deck from:abc-123] please review", nil):         TurnTriggerSend,
		fxUser("u", "please review", nil):                                   TurnTriggerHuman,
	}
	for line, want := range cases {
		f := classifyTranscriptTail([]string{line, fxAssistantText("a", "reply")})
		if f.Trigger != want {
			t.Errorf("trigger for %s = %q, want %q", line[:60], f.Trigger, want)
		}
		if want == TurnTriggerSend && f.FromID != "abc-123" {
			t.Errorf("from_id = %q", f.FromID)
		}
	}
}

func TestTextAsksParent_TolerantForms(t *testing.T) {
	yes := []string{
		"**NEED:** a ruling on the budget",
		"- NEED: merge or hold",
		"> QUESTION: which port",
		"Which port should the API use?\nThanks.",
		"Should I open 8080 too?)",
		"**Do you want the digest daily?**",
		"ask: proceed with the freeze",
	}
	no := []string{
		"All 42 tests pass.",
		"Was it flaky? Re-ran, green now.\nMerged.\nDone.",
		"The need: field is documented.",
		"Round 2 report saved, waiting for Docker.",
	}
	for _, s := range yes {
		if !textAsksParent(s) {
			t.Errorf("must read as a question: %q", s)
		}
	}
	for _, s := range no {
		if textAsksParent(s) {
			t.Errorf("must not read as a question: %q", s)
		}
	}
}

func TestCapTurnText(t *testing.T) {
	long := strings.Repeat("é", 400) // 800 bytes
	got := CapTurnText(long, 100)
	if len(got) > 100 || !strings.HasSuffix(got, "…") {
		t.Fatalf("cap: len=%d suffix=%q", len(got), got[len(got)-3:])
	}
	if CapTurnText("short", 0) != "short" {
		t.Fatal("default cap must keep short text")
	}
	if len(CapTurnText(strings.Repeat("x", 10000), 100000)) != MaxTurnTextBytes {
		t.Fatal("hard ceiling not applied")
	}
}

func TestInboxConfig_Defaults(t *testing.T) {
	var c InboxConfig
	if !c.WakesFor(TurnTierUrgent) || c.WakesFor(TurnTierInfo) || !c.WakesFor("") {
		t.Fatal("default wake_on is urgent (and legacy untiered records)")
	}
	if c.GetMaxTextBytes() != DefaultTurnTextBytes || c.GetInfoDigestMinutes() != 15 || !c.GetQuestionWakes() || c.GetJournalKeep() != DefaultTurnJournalKeep {
		t.Fatalf("defaults: %+v", c)
	}
	c.WakeOn = []string{"urgent", "info"}
	if !c.WakesFor(TurnTierInfo) {
		t.Fatal("wake_on info")
	}
}
