package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Comms redesign PR5: `session send` from inside a session prefixes one
// "[agent-deck from:<sender-id>]" line; everything that is not clearly an
// agent-to-agent prompt goes out untouched.
func TestPR5_TagSendMessage(t *testing.T) {
	base := sendTagInputs{senderID: "sender-1", senderTool: "claude", targetID: "target-1", targetTool: "claude", enabled: true}
	cases := []struct {
		name string
		msg  string
		mod  func(*sendTagInputs)
		want string
	}{
		{name: "agent send is tagged", msg: "run the tests", want: "[agent-deck from:sender-1]\nrun the tests"},
		{name: "multi-line body keeps its lines", msg: "a\nb", want: "[agent-deck from:sender-1]\na\nb"},
		{name: "human shell (no env)", msg: "hi", mod: func(in *sendTagInputs) { in.senderID = "" }, want: "hi"},
		{name: "--no-tag / tag_sends=false", msg: "hi", mod: func(in *sendTagInputs) { in.enabled = false }, want: "hi"},
		{name: "--draft", msg: "hi", mod: func(in *sendTagInputs) { in.draft = true }, want: "hi"},
		{name: "bare slash command", msg: "/compact", want: "/compact"},
		{name: "indented slash command", msg: "  /clear", want: "  /clear"},
		{name: "send to self", msg: "hi", mod: func(in *sendTagInputs) { in.targetID = "sender-1" }, want: "hi"},
		{name: "non-Claude target", msg: "ls", mod: func(in *sendTagInputs) { in.targetTool = "shell" }, want: "ls"},
		{name: "codex target", msg: "hi", mod: func(in *sendTagInputs) { in.targetTool = "codex" }, want: "hi"},
		{name: "shell sender (split pane / shell session)", msg: "hi", mod: func(in *sendTagInputs) { in.senderTool = "shell" }, want: "hi"},
		{name: "codex sender", msg: "hi", mod: func(in *sendTagInputs) { in.senderTool = "codex" }, want: "hi"},
		{name: "sender not in the target's registry", msg: "hi", mod: func(in *sendTagInputs) { in.senderTool = "" }, want: "hi"},
		{name: "conductor heartbeat", msg: session.ConductorHeartbeatMessagePrefix + " check", want: session.ConductorHeartbeatMessagePrefix + " check"},
		{name: "already enveloped", msg: "[agent-deck from:other]\nfwd", want: "[agent-deck from:other]\nfwd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			if c.mod != nil {
				c.mod(&in)
			}
			got, tagged := tagSendMessage(c.msg, in)
			if got != c.want || tagged != (c.want != c.msg) {
				t.Fatalf("tagSendMessage(%q) = %q,%v; want %q", c.msg, got, tagged, c.want)
			}
		})
	}
}

func TestPR5_SendTagsEnabledReadsConfig(t *testing.T) {
	orig := loadUserConfigForSend
	t.Cleanup(func() { loadUserConfigForSend = orig })
	off := false
	loadUserConfigForSend = func() (*session.UserConfig, error) {
		return &session.UserConfig{Send: session.SendSettings{TagSends: &off}}, nil
	}
	if sendTagsEnabled() {
		t.Fatal("tag_sends = false must disable tagging")
	}
	loadUserConfigForSend = func() (*session.UserConfig, error) { return &session.UserConfig{}, nil }
	if !sendTagsEnabled() {
		t.Fatal("tag_sends defaults to true")
	}
}

func TestPR5_SendSenderIDFromEnv(t *testing.T) {
	t.Setenv("AGENTDECK_INSTANCE_ID", " abc ")
	if got := sendSenderID(); got != "abc" {
		t.Fatalf("sendSenderID = %q", got)
	}
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	if got := sendSenderID(); got != "" {
		t.Fatalf("no env must mean no sender: %q", got)
	}
}

func TestPR5_SendSenderToolLooksUpRegistry(t *testing.T) {
	insts := []*session.Instance{{ID: "a", Tool: "claude"}, {ID: "b", Tool: "shell"}}
	if got := sendSenderTool("a", insts); got != "claude" {
		t.Fatalf("sendSenderTool(a) = %q", got)
	}
	if got := sendSenderTool("b", insts); got != "shell" {
		t.Fatalf("sendSenderTool(b) = %q", got)
	}
	if got := sendSenderTool("ghost", insts); got != "" {
		t.Fatalf("unknown sender must be \"\": %q", got)
	}
	if got := sendSenderTool("", insts); got != "" {
		t.Fatalf("no sender must be \"\": %q", got)
	}
}
