package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// claudeTurnEdge returns "prompt" for a Claude UserPromptSubmit, "stop" for
// a Claude Stop, "start" for a SessionStart (a fresh or resumed session
// starts with no unattended turn) and "" for anything else (Codex and Cursor events carry a
// turn_id or conversation_id and are not Claude turns). A pi event is not a
// Claude turn either: a pi started inside a Claude turn inherits its
// AGENTDECK_INSTANCE_ID, and its session_start must not clear that turn's
// origin.
func claudeTurnEdge(p hookPayload) string {
	if rawString(p.TurnID) != "" || p.ConversationID != "" || p.Source == "pi" {
		return ""
	}
	switch normalizeHookEventKey(p.HookEventName) {
	case "userpromptsubmit":
		return "prompt"
	case "stop":
		return "stop"
	case "sessionstart":
		// Not "compact": an auto-compaction can happen inside a turn.
		if p.Source != "compact" {
			return "start"
		}
	}
	return ""
}

// unattendedDenyDecision is the PermissionRequest answer for a turn nobody
// is watching (Claude Code: hookSpecificOutput.decision.behavior = deny,
// message shown to the model).
func unattendedDenyDecision(origin, what string) string {
	msg := fmt.Sprintf("Denied: this turn was started by %s and nobody is watching to approve %s. "+
		"Do not retry it. Write one NEED: line asking the owner for it, then stop.", origin, what)
	out, _ := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest",
			"decision":      map[string]any{"behavior": "deny", "message": msg},
		},
	})
	return string(out)
}

// permissionRequestSummary names what was asked: the Bash command, a file
// path, or the tool name.
func permissionRequestSummary(p hookPayload) string {
	tool := strings.TrimSpace(rawString(p.ToolName))
	if tool == "" {
		tool = "a tool"
	}
	var in map[string]json.RawMessage
	if json.Unmarshal(p.ToolInput, &in) == nil {
		for _, key := range []string{"command", "file_path", "url"} {
			if v := strings.Join(strings.Fields(rawString(in[key])), " "); v != "" {
				return tool + " `" + truncateWidth(v, 160) + "`"
			}
		}
	}
	return tool
}

// denyUnattendedPermission answers a permission prompt in an order or
// heartbeat turn with deny, and queues one urgent NEED for the owner on the
// conductor's human outbox (the bridge forwards urgent at once).
func denyUnattendedPermission(instanceID, origin string, p hookPayload) {
	what := permissionRequestSummary(p)
	fmt.Println(unattendedDenyDecision(origin, what))
	conductor := session.ConductorNameFromTitle(os.Getenv("AGENTDECK_TITLE"))
	if conductor == "" {
		// A renamed conductor: resolve it from the registry, as
		// resolveHumanConductor does.
		conductor = session.ConductorNameFromTitle(conductorTitleLookup(instanceID, os.Getenv("AGENTDECK_PROFILE")))
	}
	if conductor == "" {
		hookHandlerLog.Info("unattended_permission_denied",
			slog.String("instance", instanceID), slog.String("origin", origin), slog.String("what", what))
		return
	}
	need := fmt.Sprintf("NEED: %s (%s turn) wanted %s; denied because nobody was watching. Reply to allow it.",
		session.ConductorSessionTitle(conductor), origin, what)
	if _, _, err := session.AppendHumanOutbox(conductor, session.TurnTierUrgent, need); err != nil {
		hookHandlerLog.Warn("unattended_need_queue_failed", slog.String("conductor", conductor), slog.String("error", err.Error()))
	}
}
