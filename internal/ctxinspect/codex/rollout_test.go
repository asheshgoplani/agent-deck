package codex

import (
	"strings"
	"testing"
)

func TestPartTag(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "tag closed by angle bracket", text: "<multi_agent_mode>Any earlier instruction", want: "multi_agent_mode"},
		{name: "tag closed by space", text: "<permissions instructions>\nFilesystem", want: "permissions"},
		{name: "tag with digits", text: "<mode2>x", want: "mode2"},
		{name: "leading whitespace tolerated", text: "\n  <apps_instructions>\n## Apps", want: "apps_instructions"},
		{name: "agents md heading", text: "# AGENTS.md instructions\n\n<INSTRUCTIONS>", want: agentsMDTag},
		{name: "agents md heading with path", text: "# AGENTS.md instructions for /tmp/x\n", want: agentsMDTag},
		{name: "typed message is untagged", text: "Review the current integration", want: ""},
		{name: "html-ish uppercase is not a tag", text: "<INSTRUCTIONS>hello", want: ""},
		{name: "empty tag is not a tag", text: "<>x", want: ""},
		{name: "unterminated tag is not a tag", text: "<permissions", want: ""},
		{name: "comparison operator is not a tag", text: "<3 is a heart", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := partTag(tc.text); got != tc.want {
				t.Fatalf("partTag(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestParseHeadSplitsPrefixFromTypedMessage(t *testing.T) {
	typed := "Review the pi voice integration"
	path := newRollout(t).
		sessionMeta("sess-1", "/tmp/project", "BASE PROMPT").
		message(roleDeveloper, "<permissions instructions>\npolicy", "<skills_instructions>\n## Skills\n").
		message(roleUser, "# AGENTS.md instructions\n\nproject rules", "<environment_context>\n  <cwd>/tmp</cwd>").
		message(roleUser, typed).
		userMessageEvent(typed).
		assistant().
		tokenCount(21579, 21579, 258400).
		write()

	head, err := ParseHead(path)
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", head.SessionID)
	}
	if head.BaseInstructions != "BASE PROMPT" {
		t.Errorf("BaseInstructions = %q", head.BaseInstructions)
	}
	if len(head.Prefix) != 4 {
		t.Fatalf("got %d injected parts, want 4: %+v", len(head.Prefix), head.Prefix)
	}
	wantTags := []string{"permissions", skillsInstructionTag, agentsMDTag, environmentTag}
	for i, want := range wantTags {
		if head.Prefix[i].Tag != want {
			t.Errorf("part %d tag = %q, want %q", i, head.Prefix[i].Tag, want)
		}
	}
	if len(head.Excluded) != 1 || head.Excluded[0].Text != typed {
		t.Fatalf("the typed message must be excluded from the prefix, got %+v", head.Excluded)
	}
	if !head.ReachedTurn {
		t.Error("ReachedTurn = false, want true")
	}
	if head.FirstTurn == nil || head.FirstTurn.InputTokens != 21579 {
		t.Fatalf("FirstTurn = %+v, want 21579 input tokens", head.FirstTurn)
	}
	if head.FirstTurn.ContextWindow != 258400 {
		t.Errorf("ContextWindow = %d, want 258400", head.FirstTurn.ContextWindow)
	}
	if !head.Anchorable() {
		t.Errorf("Anchorable = false, want true (signals: %v)", head.ResumeSignals)
	}
}

func TestParseHeadTypedMessageWithTagStillExcluded(t *testing.T) {
	// A typed message that happens to open with an angle bracket must not be
	// promoted to injected context: the harness's own user_message event is
	// authoritative over the marker heuristic.
	typed := "<permissions instructions> what does this mean?"
	path := newRollout(t).
		sessionMeta("sess-2", "/tmp/p", "BASE").
		message(roleUser, typed).
		userMessageEvent(typed).
		assistant().
		tokenCount(10, 10, 1000).
		write()

	head, err := ParseHead(path)
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if len(head.Prefix) != 0 {
		t.Fatalf("prefix must be empty, got %+v", head.Prefix)
	}
	if len(head.Excluded) != 1 {
		t.Fatalf("the typed message must be excluded, got %+v", head.Excluded)
	}
}

func TestParseHeadRefusesAnchorOnAWarmFirstTurn(t *testing.T) {
	path := newRollout(t).
		sessionMeta("sess-3", "/tmp/p", "BASE").
		message(roleDeveloper, "<permissions instructions>\npolicy").
		assistant().
		tokenCount(1000, 940000, 258400).
		write()

	head, err := ParseHead(path)
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.FirstTurn == nil {
		t.Fatal("FirstTurn must still be read so the window is available")
	}
	if head.Anchorable() {
		t.Fatal("a turn whose cumulative prompt exceeds its own must not anchor")
	}
	if len(head.ResumeSignals) != 1 || !strings.Contains(head.ResumeSignals[0], "940000") {
		t.Fatalf("ResumeSignals = %v, want one naming the cumulative figure", head.ResumeSignals)
	}
}

func TestParseHeadSkipsAccountingRecordsWithNoUsage(t *testing.T) {
	path := newRollout(t).
		sessionMeta("sess-4", "/tmp/p", "BASE").
		message(roleDeveloper, "<permissions instructions>\npolicy").
		assistant().
		nullTokenCount().
		nullTokenCount().
		tokenCount(4242, 4242, 128000).
		write()

	head, err := ParseHead(path)
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.FirstTurn == nil || head.FirstTurn.InputTokens != 4242 {
		t.Fatalf("FirstTurn = %+v, want the first populated record (4242)", head.FirstTurn)
	}
}

func TestParseHeadStopsAtTheFirstAccountingRecord(t *testing.T) {
	// The scan must not walk the whole conversation. Records after the anchor
	// exist in the file and must never be read.
	b := newRollout(t).
		sessionMeta("sess-5", "/tmp/p", "BASE").
		message(roleDeveloper, "<permissions instructions>\npolicy").
		assistant().
		tokenCount(500, 500, 100000)
	for i := 0; i < 50; i++ {
		b.message(roleUser, "later conversation turn")
	}
	head, err := ParseHead(b.write())
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.LinesRead > 8 {
		t.Fatalf("LinesRead = %d, want the scan to stop at the anchor", head.LinesRead)
	}
	if len(head.Prefix) != 1 {
		t.Fatalf("post-boundary messages leaked into the prefix: %+v", head.Prefix)
	}
}

func TestParseHeadReadsWorldStateAndTurnContext(t *testing.T) {
	path := newRollout(t).
		sessionMeta("sess-6", "/tmp/p", "BASE").
		worldState(map[string]any{
			"agents_md":   map[string]any{"text": "PROJECT RULES"},
			"host_skills": map[string]any{"body": "## Skills\n", "includeInstructions": true},
			"environments": map[string]any{
				"current_date": "2026-07-28",
				"filesystem":   "<filesystem><workspace_roots/></filesystem>",
			},
			"permissions":       "32ca25d6d85edaeb6f1e19b2a5cc4e08d2a631cd",
			"apps_instructions": true,
		}).
		turnContext("gpt-5.6-sol", "default").
		assistant().
		tokenCount(10, 10, 1000).
		write()

	head, err := ParseHead(path)
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.World == nil {
		t.Fatal("World is nil")
	}
	if head.World.AgentsMD != "PROJECT RULES" {
		t.Errorf("AgentsMD = %q", head.World.AgentsMD)
	}
	if head.World.HostSkills != "## Skills\n" {
		t.Errorf("HostSkills = %q", head.World.HostSkills)
	}
	if head.World.PermissionsDigest == "" {
		t.Error("a digest-only permissions field must be recorded as a digest, not priced as text")
	}
	if !strings.Contains(head.World.EnvironmentsFilesystem, "workspace_roots") {
		t.Errorf("EnvironmentsFilesystem = %q", head.World.EnvironmentsFilesystem)
	}
	wantKeys := "agents_md, apps_instructions, environments, host_skills, permissions"
	if got := describeKeys(head.World.Keys); got != wantKeys {
		t.Errorf("keys = %q, want %q (sorted, so a report is stable across runs)", got, wantKeys)
	}
	if head.Turn == nil || head.Turn.Model != "gpt-5.6-sol" || head.Turn.CollaborationMode != "default" {
		t.Fatalf("Turn = %+v", head.Turn)
	}
}

func TestParseHeadReadsLegacyShapes(t *testing.T) {
	// Releases up to roughly 0.10x wrote a bare instructions string and no
	// world_state at all. Reading them is what stops an old session rendering
	// as an empty inventory.
	path := newRollout(t).
		add(recordSessionMeta, map[string]any{
			"id":           "legacy-1",
			"cwd":          "/tmp/p",
			"cli_version":  "0.77.0",
			"instructions": "## Skills\nlegacy text",
		}).
		message(roleUser, "# AGENTS.md instructions for /tmp/p\n\nrules").
		userMessageEvent("hi").
		message(roleUser, "hi").
		assistant().
		write()

	head, err := ParseHead(path)
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.SessionID != "legacy-1" {
		t.Errorf("SessionID = %q, want the id field when session_id is absent", head.SessionID)
	}
	if head.LegacyInstructions != "## Skills\nlegacy text" {
		t.Errorf("LegacyInstructions = %q", head.LegacyInstructions)
	}
	if head.BaseInstructions != "" {
		t.Errorf("BaseInstructions = %q, want empty: this release records none", head.BaseInstructions)
	}
	if head.World != nil {
		t.Error("World must stay nil when the release writes no world_state")
	}
	if len(head.Prefix) != 1 || head.Prefix[0].Tag != agentsMDTag {
		t.Fatalf("Prefix = %+v", head.Prefix)
	}
}

func TestParseHeadReadsBareStringBaseInstructions(t *testing.T) {
	path := newRollout(t).
		add(recordSessionMeta, map[string]any{
			"session_id":        "sess-7",
			"base_instructions": "PROMPT AS A BARE STRING",
		}).
		assistant().
		write()

	head, err := ParseHead(path)
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.BaseInstructions != "PROMPT AS A BARE STRING" {
		t.Fatalf("BaseInstructions = %q", head.BaseInstructions)
	}
}

func TestParseHeadRecordsMalformedRecordsAsWarnings(t *testing.T) {
	// A record we cannot read is a hole in the inventory. It must surface as a
	// warning, never as a silently empty section.
	path := newRollout(t).
		sessionMeta("sess-8", "/tmp/p", "BASE").
		addRaw("{not json").
		add(recordWorldState, map[string]any{"full": true, "state": "not-an-object"}).
		message(roleDeveloper, "<permissions instructions>\npolicy").
		assistant().
		tokenCount(10, 10, 1000).
		write()

	head, err := ParseHead(path)
	if err != nil {
		t.Fatalf("ParseHead must not fail on a malformed record: %v", err)
	}
	if len(head.Warnings) != 2 {
		t.Fatalf("Warnings = %v, want one per unreadable record", head.Warnings)
	}
	if head.BaseInstructions != "BASE" || len(head.Prefix) != 1 {
		t.Error("a malformed record must not abort the parse of the records around it")
	}
}

func TestParseHeadMissingFileIsAnError(t *testing.T) {
	if _, err := ParseHead("/nonexistent/rollout.jsonl"); err == nil {
		t.Fatal("a missing rollout must be an error, not an empty report")
	}
}

func TestContentPartRecognised(t *testing.T) {
	tests := []struct {
		name string
		part ContentPart
		want bool
	}{
		{name: "developer untagged is injected", part: ContentPart{Role: roleDeveloper}, want: true},
		{name: "user tagged is injected", part: ContentPart{Role: roleUser, Tag: environmentTag}, want: true},
		{name: "user untagged is the typed message", part: ContentPart{Role: roleUser}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.part.Recognised(); got != tc.want {
				t.Fatalf("Recognised() = %v, want %v", got, tc.want)
			}
		})
	}
}
