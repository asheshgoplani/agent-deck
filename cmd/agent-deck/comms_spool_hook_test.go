package main

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Comms Ledger producers (docs/comms.md): every hook agent-deck already
// installs forwards the text it receives to the daemon's spool, and only
// with [comms] ledger on. The hook never writes the ledger itself.

func runHookHandlerWith(t *testing.T, payload string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString(payload)
	_ = w.Close()
	restore := withStdin(t, r)
	defer restore()
	handleHookHandler()
}

func TestHookHandler_SpoolsTextPerHarness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-spool")
	t.Cleanup(session.SetCommsLedgerForTest(true))

	cases := []struct {
		name, payload, harness, edge, text, prompt string
	}{
		{"claude prompt", `{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"[agent-deck from:p1] go"}`, "claude", session.CommsEdgePromptStart, "", "[agent-deck from:p1] go"},
		{"claude stop", `{"hook_event_name":"Stop","session_id":"s1","last_assistant_message":"done","transcript_path":"/nowhere/x.jsonl"}`, "claude", session.CommsEdgeTurnEnd, "done", ""},
		{"gemini before", `{"hook_event_name":"BeforeAgent","session_id":"g1","prompt":"hi gemini"}`, "gemini", session.CommsEdgePromptStart, "", "hi gemini"},
		{"gemini after", `{"hook_event_name":"AfterAgent","session_id":"g1","prompt":"hi gemini","prompt_response":"hello back"}`, "gemini", session.CommsEdgeTurnEnd, "hello back", "hi gemini"},
		{"cursor prompt", `{"hook_event_name":"beforeSubmitPrompt","conversation_id":"c1","prompt":"cursor q"}`, "cursor", session.CommsEdgePromptStart, "", "cursor q"},
		{"cursor text", `{"hook_event_name":"afterAgentResponse","conversation_id":"c1","text":"cursor a"}`, "cursor", session.CommsEdgeTurnEnd, "cursor a", ""},
		{"hermes pre", `{"hook_event_name":"pre_llm_call","session_id":"h1","user_message":"hermes q"}`, "hermes", session.CommsEdgePromptStart, "", "hermes q"},
		{"hermes post", `{"hook_event_name":"post_llm_call","session_id":"h1","user_message":"hermes q","assistant_response":"hermes a"}`, "hermes", session.CommsEdgeTurnEnd, "hermes a", "hermes q"},
		{"pi input", `{"hook_event_name":"input","source":"pi","prompt":"pi q"}`, "pi", session.CommsEdgePromptStart, "", "pi q"},
		{"pi settled", `{"hook_event_name":"agent_settled","source":"pi","text":"pi a"}`, "pi", session.CommsEdgeTurnEnd, "pi a", ""},
	}
	for i, c := range cases {
		runHookHandlerWith(t, c.payload)
		entries, err := session.ReadCommsSpool("inst-spool")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != i+1 {
			t.Fatalf("%s: %d entries after %d hooks", c.name, len(entries), i+1)
		}
		e := entries[i]
		if e.Harness != c.harness || e.Edge != c.edge || e.Text != c.text || e.Prompt != c.prompt {
			t.Fatalf("%s: spooled %+v", c.name, e)
		}
		if c.name == "claude stop" && e.TranscriptPath != "" {
			t.Fatalf("a transcript path outside the Claude roots must not be forwarded: %q", e.TranscriptPath)
		}
	}

	// Events that carry no text for the ledger spool nothing: a Cursor stop
	// (status only), a tool event, a notification.
	before := len(cases)
	for _, payload := range []string{
		`{"hook_event_name":"stop","conversation_id":"c1"}`,
		`{"hook_event_name":"PreToolUse","session_id":"s1"}`,
		`{"hook_event_name":"turn_end","source":"pi"}`,
		`{"hook_event_name":"Stop","session_id":"s1","last_assistant_message":"   "}`,
	} {
		runHookHandlerWith(t, payload)
	}
	if entries, _ := session.ReadCommsSpool("inst-spool"); len(entries) != before {
		t.Fatalf("text-less events spooled: %d entries, want %d", len(entries), before)
	}
}

func TestHookHandler_SpoolsNothingWithLedgerOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-off")
	t.Cleanup(session.SetCommsLedgerForTest(false))
	runHookHandlerWith(t, `{"hook_event_name":"Stop","session_id":"s1","last_assistant_message":"done"}`)
	if entries, _ := session.ReadCommsSpool("inst-off"); len(entries) != 0 {
		t.Fatalf("spooled with the ledger off: %+v", entries)
	}
	if _, err := os.Stat(session.CommsSpoolDir()); err == nil {
		t.Fatal("spool directory created with the ledger off")
	}
}

func TestCodexNotify_SpoolsLastAssistantMessage(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-codex")
	t.Setenv("CODEX_SESSION_ID", "")
	t.Cleanup(session.SetCommsLedgerForTest(true))
	seedCodexNotifyRollout(t, tmpHome, "thread-9")

	origArgs := os.Args
	defer func() { os.Args = origArgs }()
	os.Args = []string{"agent-deck", "codex-notify",
		`{"type":"agent-turn-complete","thread-id":"thread-9","turn-id":"turn-3","cwd":"/tmp/w","input-messages":["first","[HEARTBEAT] anything?"],"last-assistant-message":"Nothing new."}`}
	handleCodexNotify()

	entries, err := session.ReadCommsSpool("inst-codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries: %+v", entries)
	}
	e := entries[0]
	if e.Harness != "codex" || e.Edge != session.CommsEdgeTurnEnd || e.Text != "Nothing new." || e.Prompt != "[HEARTBEAT] anything?" ||
		e.SessionID != "thread-9" || e.TurnID != "turn-3" || e.Cwd != "/tmp/w" {
		t.Fatalf("spooled %+v", e)
	}

	// A turn start carries no text: nothing spooled.
	os.Args = []string{"agent-deck", "codex-notify", `{"type":"turn/started","thread-id":"thread-9","turn-id":"turn-4"}`}
	handleCodexNotify()
	if entries, _ := session.ReadCommsSpool("inst-codex"); len(entries) != 1 {
		t.Fatalf("turn start spooled: %+v", entries)
	}
}
