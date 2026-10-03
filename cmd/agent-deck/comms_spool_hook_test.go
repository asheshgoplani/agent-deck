package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Comms Ledger producers (docs/comms.md). P1 enables two: Claude through
// hook-handler and Codex through codex-notify, each pinned by a versioned
// payload fixture under testdata/comms. Every other harness payload
// spools nothing (status-only in P1), and a Codex Stop hook never spools
// here, so notify and Stop cannot produce one turn twice. The hook never
// writes the ledger itself.

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

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "comms", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestHookHandler_ClaudeFixturesSpoolBothEdges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-spool")
	t.Cleanup(session.SetCommsLedgerForTest(true))

	runHookHandlerWith(t, fixture(t, "claude_userpromptsubmit_v1.json"))
	runHookHandlerWith(t, fixture(t, "claude_stop_v1.json"))
	entries, err := session.ReadCommsSpool("inst-spool")
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries %+v err %v", entries, err)
	}
	ups, stop := entries[0], entries[1]
	if ups.Harness != "claude" || ups.Edge != session.CommsEdgePromptStart || ups.Prompt != "[agent-deck from:conductor-1] build it" || ups.SessionID != "8f3c2a1e-0000-4000-8000-000000000001" {
		t.Fatalf("UserPromptSubmit: %+v", ups)
	}
	if stop.Harness != "claude" || stop.Edge != session.CommsEdgeTurnEnd || stop.Text == "" || stop.TurnID != "" || stop.Prompt != "" {
		t.Fatalf("Stop: %+v", stop)
	}
	if stop.TranscriptPath != "" {
		t.Fatalf("a transcript path outside the Claude roots must not be forwarded: %q", stop.TranscriptPath)
	}
	if stop.Cwd != "/tmp" || stop.TSignal == 0 {
		t.Fatalf("Stop metadata: %+v", stop)
	}
}

func TestHookHandler_OnlyClaudeSpoolsInP1(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-others")
	t.Cleanup(session.SetCommsLedgerForTest(true))
	for name, payload := range map[string]string{
		"codex stop (notify owns the turn)": fixture(t, "codex_stop_v1.json"),
		"codex userpromptsubmit":            `{"hook_event_name":"UserPromptSubmit","session_id":"c","turn_id":"turn-78","prompt":"x"}`,
		"cursor stop":                       `{"hook_event_name":"stop","conversation_id":"c1"}`,
		"cursor afterAgentResponse":         `{"hook_event_name":"afterAgentResponse","conversation_id":"c1","text":"cursor a"}`,
		"gemini AfterAgent":                 `{"hook_event_name":"AfterAgent","session_id":"g1","prompt":"q","prompt_response":"a"}`,
		"hermes post_llm_call":              `{"hook_event_name":"post_llm_call","session_id":"h1","user_message":"q","assistant_response":"a"}`,
		"pi turn_end":                       `{"hook_event_name":"turn_end","source":"pi","text":"a"}`,
		"claude tool event":                 `{"hook_event_name":"PreToolUse","session_id":"s1"}`,
		"claude empty stop":                 `{"hook_event_name":"Stop","session_id":"s1","last_assistant_message":"   "}`,
	} {
		runHookHandlerWith(t, payload)
		if entries, _ := session.ReadCommsSpool("inst-others"); len(entries) != 0 {
			t.Fatalf("%s spooled: %+v", name, entries)
		}
	}
}

func TestHookHandler_SpoolsNothingWithLedgerOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-off")
	t.Cleanup(session.SetCommsLedgerForTest(false))
	runHookHandlerWith(t, fixture(t, "claude_stop_v1.json"))
	if entries, _ := session.ReadCommsSpool("inst-off"); len(entries) != 0 {
		t.Fatalf("spooled with the ledger off: %+v", entries)
	}
	if _, err := os.Stat(session.CommsSpoolDir()); err == nil {
		t.Fatal("spool directory created with the ledger off")
	}
}

func TestHookHandler_RejectsATraversalInstanceID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "../../etc")
	t.Cleanup(session.SetCommsLedgerForTest(true))
	runHookHandlerWith(t, fixture(t, "claude_stop_v1.json"))
	if _, err := os.Stat(session.CommsSpoolDir()); err == nil {
		t.Fatal("a traversal instance id reached the spool")
	}
}

func TestCodexNotify_FixtureSpoolsLastAssistantMessageOnce(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-codex")
	t.Setenv("CODEX_SESSION_ID", "")
	t.Cleanup(session.SetCommsLedgerForTest(true))
	seedCodexNotifyRollout(t, tmpHome, "019a0000-0000-7000-8000-000000000002")

	origArgs := os.Args
	defer func() { os.Args = origArgs }()
	os.Args = []string{"agent-deck", "codex-notify", fixture(t, "codex_notify_v1.json")}
	handleCodexNotify()

	entries, err := session.ReadCommsSpool("inst-codex")
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries %+v err %v", entries, err)
	}
	e := entries[0]
	if e.Harness != "codex" || e.Edge != session.CommsEdgeTurnEnd || e.Text != "Nothing new since the last check." || e.Prompt != "[HEARTBEAT] anything new?" ||
		e.SessionID != "019a0000-0000-7000-8000-000000000002" || e.TurnID != "turn-77" || e.Cwd != "/tmp/w" {
		t.Fatalf("spooled %+v", e)
	}

	// The matching Codex Stop hook for the same turn adds nothing.
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	runHookHandlerWith(t, fixture(t, "codex_stop_v1.json"))
	// A turn start carries no text: nothing spooled.
	os.Args = []string{"agent-deck", "codex-notify", `{"type":"turn/started","thread-id":"019a0000-0000-7000-8000-000000000002","turn-id":"turn-78"}`}
	handleCodexNotify()
	if entries, _ := session.ReadCommsSpool("inst-codex"); len(entries) != 1 {
		t.Fatalf("Codex turn produced more than one spool entry: %+v", entries)
	}

	// An invalid instance id never reaches the spool: no new spool
	// directory appears anywhere, and the only instance spooled is ours.
	t.Setenv("AGENTDECK_INSTANCE_ID", "../x")
	os.Args = []string{"agent-deck", "codex-notify", fixture(t, "codex_notify_v1.json")}
	handleCodexNotify()
	if got := session.ListCommsSpoolInstances(); len(got) != 1 || got[0] != "inst-codex" {
		t.Fatalf("traversal id reached the spool: %v", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(session.CommsSpoolDir()), "x")); err == nil {
		t.Fatal("traversal id escaped the spool root")
	}
}
