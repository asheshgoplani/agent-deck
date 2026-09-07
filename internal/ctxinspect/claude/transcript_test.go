package claude

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTranscript writes JSONL records to a temp file and returns its path.
func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("writing fixture transcript: %v", err)
	}
	return path
}

// Record fixtures mirroring the shapes Claude Code actually writes, taken from
// live transcripts rather than from documentation.
const (
	recSkillListing = `{"type":"attachment","sessionId":"s1","attachment":{"type":"skill_listing",` +
		`"content":"- alpha: does alpha things.\n- beta: does beta things.","skillCount":2,"isInitial":true,` +
		`"names":["alpha","beta"]}}`
	recAgentListing = `{"type":"attachment","sessionId":"s1","attachment":{"type":"agent_listing_delta",` +
		`"addedTypes":["Explore","mine"],"addedLines":["- Explore: read-only search.","- mine: my own agent."],"isInitial":true}}`
	recDeferredTools = `{"type":"attachment","sessionId":"s1","attachment":{"type":"deferred_tools_delta",` +
		`"addedNames":["WebFetch","mcp__telegram__reply"],"removedNames":[]}}`
	recMCPInstructions = `{"type":"attachment","sessionId":"s1","attachment":{"type":"mcp_instructions_delta",` +
		`"addedNames":["plugin:telegram:telegram"],"addedBlocks":["## telegram\nReply through the tool."]}}`
	recNestedMemory = `{"type":"attachment","sessionId":"s1","attachment":{"type":"nested_memory",` +
		`"path":"/etc/none/CLAUDE.md","content":{"path":"/etc/none/CLAUDE.md","content":"captured memory body"}}}`
	recHookContext = `{"type":"attachment","sessionId":"s1","attachment":{"type":"hook_additional_context",` +
		`"content":["hook says hello"],"hookName":"SessionStart"}}`
	// A `file` attachment's content is an object where a skill_listing's is a
	// string. Decoding every attachment into one struct would fail on this
	// perfectly valid record, so it is part of the fixture on purpose.
	recFileAttachment = `{"type":"attachment","sessionId":"s1","attachment":{"type":"file",` +
		`"filename":"/tmp/x.md","content":{"type":"text","file":{"filePath":"/tmp/x.md","content":"hi"}}}}`
	recUserTyped   = `{"type":"user","sessionId":"s1","promptSource":"typed","message":{"role":"user","content":"hello there"}}`
	recUserCommand = `{"type":"user","sessionId":"s1","message":{"role":"user","content":"<command-name>/model</command-name>"}}`
	recAssistant   = `{"type":"assistant","sessionId":"s1","timestamp":"2026-07-01T10:00:00.000Z",` +
		`"message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":6,` +
		`"cache_creation_input_tokens":40000,"cache_read_input_tokens":20000,"output_tokens":100}}}`
	// Newer Claude Code versions zero the top-level usage and report the real
	// figures per iteration; both shapes must be read.
	recAssistantIterations = `{"type":"assistant","sessionId":"s1","message":{"role":"assistant","model":"claude-opus-4-8",` +
		`"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,` +
		`"iterations":[{"input_tokens":2,"cache_creation_input_tokens":1000,"cache_read_input_tokens":500}]}}}`
	recCompactBoundary = `{"type":"system","subtype":"compact_boundary","sessionId":"s1","compactMetadata":{"trigger":"manual"}}`
	recCompactSummary  = `{"type":"user","sessionId":"s1","isCompactSummary":true,"message":{"role":"user","content":"summary"}}`
	recSummary         = `{"type":"summary","sessionId":"s1","summary":"earlier work"}`
)

func TestParseHeadStopsAtFirstAssistantRecord(t *testing.T) {
	// The listing after the boundary must never be read: the head parse is what
	// keeps the cost independent of conversation length, and reading past the
	// boundary would also let a mid-session delta overwrite the startup state.
	after := `{"type":"attachment","sessionId":"s1","attachment":{"type":"skill_listing",` +
		`"content":"- gamma: added later.","skillCount":1,"isInitial":false,"names":["gamma"]}}`
	path := writeTranscript(t,
		recSkillListing,
		recAssistant,
		after,
		`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","usage":{"input_tokens":999999}}}`,
	)

	head, err := ParseHead(path)
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if !head.ReachedAssistant {
		t.Fatal("expected the parser to reach the boundary record")
	}
	if head.LinesRead != 2 {
		t.Fatalf("LinesRead = %d, want 2 (one attachment plus the boundary record)", head.LinesRead)
	}
	if head.Skills == nil || len(head.Skills.Names) != 2 {
		t.Fatalf("skills = %+v, want the pre-boundary listing of two skills", head.Skills)
	}
	if got := head.FirstTurn.PromptTokens(); got != 60006 {
		t.Fatalf("PromptTokens = %d, want 60006 from the first assistant turn only", got)
	}
}

func TestParseHeadCapturesStartupInventory(t *testing.T) {
	path := writeTranscript(t,
		recDeferredTools, recAgentListing, recSkillListing, recMCPInstructions,
		recNestedMemory, recHookContext, recFileAttachment, recUserCommand, recUserTyped,
		recAssistant,
	)

	head, err := ParseHead(path)
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.SessionID != "s1" {
		t.Errorf("SessionID = %q, want %q", head.SessionID, "s1")
	}
	if head.Deferred == nil || len(head.Deferred.Names) != 2 {
		t.Errorf("deferred tools = %+v, want two names", head.Deferred)
	}
	if head.Agents == nil || len(head.Agents.Lines) != 2 {
		t.Errorf("agents = %+v, want two lines", head.Agents)
	}
	if head.MCP == nil || len(head.MCP.Blocks) != 1 || !strings.Contains(head.MCP.Blocks[0], "Reply through the tool") {
		t.Errorf("mcp instructions = %+v, want the verbatim block", head.MCP)
	}
	if got := head.NestedMemory["/etc/none/CLAUDE.md"]; got != "captured memory body" {
		t.Errorf("nested memory = %q, want the captured body", got)
	}
	if head.HookContextCount != 1 || head.HookContextChars != len("hook says hello") {
		t.Errorf("hook context = %d records / %d chars, want 1 / %d", head.HookContextCount, head.HookContextChars, len("hook says hello"))
	}
	// The slash-command envelope is harness bookkeeping, not something the user
	// can trim, so the typed message is the one that counts.
	if head.FirstUserChars != len("hello there") {
		t.Errorf("FirstUserChars = %d, want %d (the typed message, not the command envelope)", head.FirstUserChars, len("hello there"))
	}
	if len(head.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", head.Warnings)
	}
}

func TestParseHeadPrefersIterationUsage(t *testing.T) {
	head, err := ParseHead(writeTranscript(t, recAssistantIterations))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.FirstTurn == nil {
		t.Fatal("want a first turn from the iteration figures")
	}
	if got := head.FirstTurn.PromptTokens(); got != 1502 {
		t.Fatalf("PromptTokens = %d, want 1502 from iterations[0]; the zeroed top-level fields must not win", got)
	}
	if !strings.Contains(head.FirstTurn.Source, "iterations[0]") {
		t.Errorf("Source = %q, want it to name the iteration path it read", head.FirstTurn.Source)
	}
}

func TestParseHeadResumeGuard(t *testing.T) {
	tests := []struct {
		name       string
		lines      []string
		anchorable bool
	}{
		{
			// A warm prompt cache is not a resume. Anthropic's cache is shared
			// between sessions, so a genuinely fresh session reports a large
			// cache read; vetoing on it would leave every session unanchored.
			name:       "warm cache alone still anchors",
			lines:      []string{recSkillListing, recAssistant},
			anchorable: true,
		},
		{
			name:       "compaction boundary disqualifies",
			lines:      []string{recCompactBoundary, recAssistant},
			anchorable: false,
		},
		{
			name:       "compact summary message disqualifies",
			lines:      []string{recCompactSummary, recAssistant},
			anchorable: false,
		},
		{
			name:       "conversation summary record disqualifies",
			lines:      []string{recSummary, recAssistant},
			anchorable: false,
		},
		{
			name:       "no assistant turn yields no anchor",
			lines:      []string{recSkillListing},
			anchorable: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			head, err := ParseHead(writeTranscript(t, tc.lines...))
			if err != nil {
				t.Fatalf("ParseHead: %v", err)
			}
			if got := head.Anchorable(); got != tc.anchorable {
				t.Fatalf("Anchorable() = %v, want %v (signals: %v)", got, tc.anchorable, head.ResumeSignals)
			}
		})
	}
}

func TestParseHeadMalformedRecordWarnsRatherThanFails(t *testing.T) {
	// A single bad record must cost that record and nothing more. Aborting the
	// report would hide everything else the session did inject; skipping it
	// silently would present a gap as an empty section.
	head, err := ParseHead(writeTranscript(t, "{not json", recSkillListing, recAssistant))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if len(head.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", head.Warnings)
	}
	if head.Skills == nil {
		t.Fatal("the well-formed records after the bad one must still be read")
	}
}

func TestParseHeadZeroPromptTotalIsNotAnAnchor(t *testing.T) {
	// A zero prompt cannot be real; reporting it would produce an anchor of 0
	// and a residual equal to minus everything attributed.
	head, err := ParseHead(writeTranscript(t,
		`{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.FirstTurn != nil {
		t.Fatalf("FirstTurn = %+v, want none for a zero prompt total", head.FirstTurn)
	}
	if len(head.Warnings) == 0 {
		t.Error("want a warning explaining why no measured total is available")
	}
}

func TestParseHeadMissingFileIsAnError(t *testing.T) {
	if _, err := ParseHead(filepath.Join(t.TempDir(), "absent.jsonl")); err == nil {
		t.Fatal("want an error for an unreadable transcript, not an empty head")
	}
}

func TestParseHeadLargeRecordDoesNotStopTheParse(t *testing.T) {
	// A big attachment — a file record carrying a whole document, which is
	// routine — must not prevent the records after it from being read.
	big := `{"type":"attachment","attachment":{"type":"file","filename":"/tmp/x.md","content":{"type":"text","file":{"filePath":"/tmp/x.md","content":"` +
		strings.Repeat("x", 200000) + `"}}}}`
	head, err := ParseHead(writeTranscript(t, big, recSkillListing, recAssistant))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.Skills == nil {
		t.Fatal("records after a large one must still be read")
	}
	if len(head.Warnings) != 0 {
		t.Errorf("a record within the cap must not warn: %v", head.Warnings)
	}
}

func TestReadBoundedLine(t *testing.T) {
	// The cap is what stops one pathological record from costing whatever the
	// file happens to contain. bufio.Scanner cannot express this: a record over
	// its buffer aborts the entire scan.
	tests := []struct {
		name          string
		input         string
		max           int
		wantLines     []string
		wantTruncated []bool
	}{
		{
			name:          "short lines pass through",
			input:         "ab\ncd\n",
			max:           16,
			wantLines:     []string{"ab\n", "cd\n"},
			wantTruncated: []bool{false, false},
		},
		{
			name:          "oversized line is capped and flagged",
			input:         strings.Repeat("x", 40) + "\nok\n",
			max:           8,
			wantLines:     []string{strings.Repeat("x", 8), "ok\n"},
			wantTruncated: []bool{true, false},
		},
		{
			name:          "final line without a newline",
			input:         "tail",
			max:           16,
			wantLines:     []string{"tail"},
			wantTruncated: []bool{false},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := bufio.NewReaderSize(strings.NewReader(tc.input), 16)
			for i := range tc.wantLines {
				line, truncated, err := readBoundedLine(r, tc.max)
				if err != nil && err != io.EOF {
					t.Fatalf("line %d: %v", i, err)
				}
				if string(line) != tc.wantLines[i] {
					t.Errorf("line %d = %q, want %q", i, line, tc.wantLines[i])
				}
				if truncated != tc.wantTruncated[i] {
					t.Errorf("line %d truncated = %v, want %v", i, truncated, tc.wantTruncated[i])
				}
			}
		})
	}
}

func TestParseHeadIgnoresLaterNonInitialListing(t *testing.T) {
	// A delta describes a change to the catalogue, not the startup state.
	later := `{"type":"attachment","attachment":{"type":"skill_listing","content":"- gamma: later.","skillCount":1,"isInitial":false,"names":["gamma"]}}`
	head, err := ParseHead(writeTranscript(t, recSkillListing, later, recAssistant))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.Skills == nil || len(head.Skills.Names) != 2 || head.Skills.Names[0] != "alpha" {
		t.Fatalf("skills = %+v, want the initial listing to win", head.Skills)
	}
}

// recAssistantPreBlock is the turn Claude Code records *before* it writes the
// startup catalogues — smaller, because its request did not carry them.
const recAssistantPreBlock = `{"type":"assistant","sessionId":"s1","timestamp":"2026-07-01T09:59:00.000Z",` +
	`"message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":1530,` +
	`"cache_creation_input_tokens":0,"cache_read_input_tokens":48692,"output_tokens":10}}}`

func TestParseHeadFindsStartupBlockInjectedAfterTheFirstTurn(t *testing.T) {
	// The shape 76 of 225 real transcripts have: an initial turn, then the
	// catalogues, then the turn whose request actually carried them. Stopping at
	// the first turn loses every catalogue and reads the anchor from a request
	// that did not include them.
	toolResult := `{"type":"user","sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`
	head, err := ParseHead(writeTranscript(t,
		recAssistantPreBlock,
		toolResult,
		recDeferredTools, recAgentListing, recSkillListing,
		recAssistant,
		`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","usage":{"input_tokens":999999}}}`,
	))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if !head.ReachedAssistant || !head.StartupBlockObserved {
		t.Fatalf("reached = %v, startup block observed = %v; want both", head.ReachedAssistant, head.StartupBlockObserved)
	}
	if head.Skills == nil || len(head.Skills.Names) != 2 {
		t.Fatalf("skills = %+v, want the catalogue injected after the first turn", head.Skills)
	}
	if head.Deferred == nil || head.Agents == nil {
		t.Fatalf("deferred = %+v, agents = %+v; want both halves of the block", head.Deferred, head.Agents)
	}
	if head.PreBlockTurns != 1 {
		t.Errorf("PreBlockTurns = %d, want 1", head.PreBlockTurns)
	}
	// The anchor must be the turn that carried the catalogues, not the earlier,
	// smaller one, and not the turn after the boundary.
	if got := head.FirstTurn.PromptTokens(); got != 60006 {
		t.Fatalf("PromptTokens = %d, want 60006 from the turn that carried the startup block", got)
	}
	if !head.Anchorable() {
		t.Errorf("want an anchor; signals: %v", head.ResumeSignals)
	}
	if head.LinesRead != 6 {
		t.Errorf("LinesRead = %d, want 6: the parse must stop at the boundary", head.LinesRead)
	}
}

func TestParseHeadWithholdsAnchorWhenNoStartupBlockPrecedesTheTurn(t *testing.T) {
	// A request cannot have carried a catalogue that is not in the transcript,
	// so its accounting does not measure a cold prefix. This is the Claude
	// counterpart of the Codex cumulative-vs-turn cold-start guard.
	head, err := ParseHead(writeTranscript(t, recAssistantPreBlock))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.StartupBlockObserved {
		t.Fatal("no startup block exists in this fixture")
	}
	if head.FirstTurn == nil {
		t.Fatal("want the turn kept for its model name")
	}
	if head.Anchorable() {
		t.Fatal("want no anchor: the turn predates any recorded startup injection")
	}
	if len(head.ResumeSignals) == 0 {
		t.Error("want a resume signal explaining why the turn is not a cold measurement")
	}
}

func TestParseHeadLookaheadRejectsAMidConversationCatalogue(t *testing.T) {
	// A catalogue re-injected deep into a conversation is not startup state:
	// anchoring on the turn after it would fold the whole conversation into the
	// fixed prefix. Two real transcripts do exactly this, 52 turns in.
	lines := []string{recAssistantPreBlock}
	for i := 0; i < maxStartupLookaheadTurns+2; i++ {
		lines = append(lines,
			`{"type":"user","sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
			// A distinct accounting per turn, so these count as separate turns.
			`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","usage":{"input_tokens":`+
				string(rune('1'+i))+`0000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`)
	}
	lines = append(lines, recSkillListing, recAssistant)

	head, err := ParseHead(writeTranscript(t, lines...))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.StartupBlockObserved {
		t.Fatal("the late catalogue must fall outside the bounded lookahead")
	}
	if head.Skills != nil {
		t.Fatalf("skills = %+v, want none: a mid-conversation catalogue is not startup state", head.Skills)
	}
	if head.Anchorable() {
		t.Fatalf("want no anchor; signals: %v", head.ResumeSignals)
	}
}

func TestParseHeadCountsOneTurnPerAccounting(t *testing.T) {
	// Claude Code writes several assistant records for one model turn — thinking,
	// text and tool_use blocks land separately carrying identical usage. Counting
	// records instead of turns would exhaust the lookahead on a single turn.
	lines := []string{recAssistantPreBlock, recAssistantPreBlock, recAssistantPreBlock, recSkillListing, recAssistant}
	head, err := ParseHead(writeTranscript(t, lines...))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.PreBlockTurns != 1 {
		t.Errorf("PreBlockTurns = %d, want 1: three records of one turn are one turn", head.PreBlockTurns)
	}
	if head.Skills == nil {
		t.Fatal("want the catalogue found after the repeated records of a single turn")
	}
}
