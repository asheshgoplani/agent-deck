package main

import (
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/health"

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

// tagSendMessage prepends the sender envelope to ordinary agent-to-agent
// messages. Claude-to-Claude sends retain automatic reply routing; Pi sends
// carry source attribution so the receiver can explicitly send a result back.
// The tag is applied before transport and length guards.
//
// Human shells, opt-outs, drafts, self-sends, commands, heartbeats and messages
// already carrying an envelope are unchanged. Pi attribution is restricted to
// Pi and Claude-compatible targets: never type an envelope into a shell.
func tagSendMessage(message string, in sendTagInputs) (string, bool) {
	sender := strings.TrimSpace(in.senderID)
	switch {
	case !in.enabled, in.draft, sender == "", sender == in.targetID,
		!supportsSendAttribution(in.senderTool, in.targetTool),
		isBareSlashCommand(message),
		in.targetTool == "pi" && strings.HasPrefix(strings.TrimSpace(message), "!"),
		session.IsConductorHeartbeatMessage(message),
		session.HasSendEnvelope(message):
		return message, false
	}
	return session.SendEnvelope(sender) + "\n" + message, true
}

// supportsSendAttribution keeps the existing Claude routing contract separate
// from Pi's source-only attribution. It does not opt Pi into Claude transport,
// transcript parsing, or automatic reply delivery.
func supportsSendAttribution(senderTool, targetTool string) bool {
	if senderTool == "pi" {
		return targetTool == "pi" || session.IsClaudeCompatible(targetTool)
	}
	return session.IsClaudeCompatible(senderTool) && session.IsClaudeCompatible(targetTool)
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

// ledgerSendVia is the transport a send record names before delivery.
func ledgerSendVia(inst *session.Instance) string {
	if inst != nil && inst.IsSSH() {
		return "ssh"
	}
	return "tmux"
}

// ledgerDeliveryState maps a send's delivery classification onto the
// ledger's delivery states: landed (submission confirmed), typed (sent,
// landing not observed), failed.
func ledgerDeliveryState(delivery string, sendErr error) string {
	switch journalSendOutcome(delivery, sendErr) {
	case health.SendConfirmed:
		return comms.StateLanded
	case health.SendUnconfirmed:
		return comms.StateTyped
	}
	return comms.StateFailed
}

func ledgerDeliveryReason(sendErr error) string {
	if sendErr == nil {
		return ""
	}
	return firstLineOf(sendErr.Error())
}

// ledgerSendAllowed reports whether this send is a message to record as a
// send: not a machine wake line (the daemon's nudges carry
// session.MachineSendEnv, and are wake records already).
func ledgerSendAllowed() bool {
	return os.Getenv(session.MachineSendEnv) != "1"
}

// launchKickoffTagInputs is what tagLaunchKickoff decides on.
type launchKickoffTagInputs struct {
	senderID  string            // the caller's AGENTDECK_INSTANCE_ID; "" for a human shell
	parent    *session.Instance // the session launch links the child to; nil for top-level
	childTool string
	enabled   bool // [send] tag_sends, minus launch --no-tag
	// childReportsTurns is false for a child launched with
	// --no-transition-notify or with [notifications] transition events off.
	childReportsTurns bool
}

// tagLaunchKickoff prepends the send envelope to a launch's initial message
// when the session running `launch` is the parent the child is linked to
// (issue #3131). That kickoff is the parent asking its own child, exactly
// what a tagged `session send` from the parent is, so the child's first turn
// (often its whole job) is classified as a send from the parent, and an
// urgent result wakes an idle Claude parent under the existing rule for a
// parent that asked. It is never tagged when the caller is not the linked
// parent (a human shell, --parent naming another session, a nested launch
// linked to the caller's own parent), the parent has no prompt-time drain,
// or the child's turns are not reported to its parent (--no-transition-notify
// or transition events off: its identity block tells it to answer an
// enveloped message with `session send`, which would type its whole answer
// into the parent, and no record is sent that a tag could make a wake);
// every other exclusion is tagSendMessage's.
func tagLaunchKickoff(message string, in launchKickoffTagInputs) (string, bool) {
	sender := strings.TrimSpace(in.senderID)
	if strings.TrimSpace(message) == "" || in.parent == nil || sender == "" || sender != in.parent.ID || !in.childReportsTurns || !session.IsClaudeCompatible(in.parent.Tool) {
		return message, false
	}
	return tagSendMessage(message, sendTagInputs{
		senderID:   sender,
		senderTool: in.parent.Tool,
		targetTool: in.childTool,
		enabled:    in.enabled,
	})
}
