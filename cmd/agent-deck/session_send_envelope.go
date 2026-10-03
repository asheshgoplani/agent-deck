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
// a non-Claude target (only Claude transcripts are classified, and a
// newline into a shell pane would run the tag as a command), a bare slash
// command (a prefix would stop it executing), a conductor heartbeat (its
// prefix is what the heartbeat skip and the classifier key on) and a
// message that already carries an envelope.
func tagSendMessage(message string, in sendTagInputs) (string, bool) {
	sender := strings.TrimSpace(in.senderID)
	switch {
	case !in.enabled, in.draft, sender == "", sender == in.targetID,
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
