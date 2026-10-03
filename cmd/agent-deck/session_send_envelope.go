package main

import (
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// sendTagInputs is everything tagSendMessage decides on. Kept a plain value
// so the decision is table-testable without tmux or a registry.
type sendTagInputs struct {
	senderID   string // the caller's AGENTDECK_INSTANCE_ID; "" for a human shell
	senderTool string // the sender's tool in the target's registry; "" when not found
	targetID   string
	targetTool string
	enabled    bool // [send] tag_sends, minus --no-tag and the queue worker
	draft      bool
}

// tagSendMessage prepends the "[agent-deck from:<sender-id>]" envelope line
// to a send made from inside an agent-deck session, so the receiver's reply
// turn is classified as a send (not a human prompt) and routed back to the
// sender (comms redesign PR5). It returns the message to deliver and whether
// it was tagged. The tag is applied before any transport or line-length
// guard, so it counts toward every limit the delivery checks.
//
// When in doubt it does not tag: a human shell (no sender id), --no-tag or
// tag_sends = false, a --draft the operator will review, a send to oneself,
// a sender that is not a Claude-compatible session in the target's registry
// (the reply is routed back only to a session whose prompt-time drain can
// inject it; a wake line typed into a shell or another harness would run or
// strand it, so the receiver's "the sender is notified" would be false),
// a non-Claude target (only Claude transcripts are classified, and a
// newline into a shell pane would run the tag as a command), a bare slash
// command (a prefix would stop it executing), a conductor heartbeat (its
// prefix is what the heartbeat skip and the classifier key on) and a
// message that already carries an envelope.
func tagSendMessage(message string, in sendTagInputs) (string, bool) {
	sender := strings.TrimSpace(in.senderID)
	switch {
	case !in.enabled, in.draft, sender == "", sender == in.targetID,
		!session.IsClaudeCompatible(in.senderTool),
		!session.IsClaudeCompatible(in.targetTool),
		isBareSlashCommand(message),
		session.IsConductorHeartbeatMessage(message),
		session.HasSendEnvelope(message):
		return message, false
	}
	return session.SendEnvelope(sender) + "\n" + message, true
}

// sendTagsEnabled resolves [send] tag_sends for one send; an unreadable
// config keeps the default (true), like every other [send] read.
func sendTagsEnabled() bool {
	cfg, _ := loadUserConfigForSend()
	return cfg.GetTagSends()
}

// sendSenderID is the calling session's id, "" outside an agent-deck session.
func sendSenderID() string {
	return strings.TrimSpace(os.Getenv("AGENTDECK_INSTANCE_ID"))
}

// sendSenderTool is the tool of the calling session as the target's registry
// knows it; "" when the sender is not in it (another profile, removed).
func sendSenderTool(senderID string, instances []*session.Instance) string {
	if senderID == "" {
		return ""
	}
	for _, inst := range instances {
		if inst != nil && inst.ID == senderID {
			return inst.Tool
		}
	}
	return ""
}
