package session

import (
	"bytes"
	"encoding/json"
	"strings"
)

// portableContextProjection deliberately exports only readable main
// conversation turns. It is intentionally conservative: unknown record and
// content shapes are omitted rather than serialized wholesale, because native
// JSONL commonly contains instructions, attachment references, account data,
// bridge metadata, and tool-private state.
// projectPortableContext projects every readable user/assistant turn from the
// validated exact artifact. Callers must apply their transfer budget after this
// step: tail-cutting raw JSONL first can leave only giant metadata records and
// silently discard the conversation that preceded them.
func projectPortableContext(source string, payload []byte) (string, bool) {
	var turns []string
	for _, line := range bytes.Split(payload, []byte("\n")) {
		role, text, ok := portableConversationTurn(line)
		if canonicalSwitchHarness(source) == "pi" {
			role, text, ok = portablePiConversationTurn(line)
		}
		if !ok {
			continue
		}
		// Codex represents injected workspace instructions/environment as user
		// messages. They are attachments, not the user's conversation turns.
		if canonicalSwitchHarness(source) == "codex" && role == "user" &&
			(strings.HasPrefix(text, "# AGENTS.md instructions") ||
				strings.HasPrefix(text, "<environment_context>") ||
				strings.HasPrefix(text, "<permissions instructions>")) {
			continue
		}
		turns = append(turns, "["+strings.ToUpper(role)+"]\n"+text)
	}
	return strings.Join(turns, "\n\n"), len(turns) != 0
}

// portableContextProjection is retained for display-only callers. Transfer
// code must use projectPortableContext so an empty projection is refused before
// target creation rather than injected as a plausible context message.
func portableContextProjection(source string, payload []byte) string {
	projection, ok := projectPortableContext(source, payload)
	if !ok {
		return "[No readable user or assistant text was available in the exact source artifact.]"
	}
	return projection
}

// portablePiConversationTurn accepts Pi's message envelope only. In
// particular it does not fall back to record type as a role, which keeps Pi
// usertool/toolCall records and session metadata out of transferred context.
func portablePiConversationTurn(line []byte) (string, string, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return "", "", false
	}
	var record struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(line, &record) != nil || record.Type != "message" || len(record.Message) == 0 {
		return "", "", false
	}
	var message map[string]json.RawMessage
	if json.Unmarshal(record.Message, &message) != nil {
		return "", "", false
	}
	role := strings.ToLower(strings.TrimSpace(portableString(message["role"])))
	if role != "user" && role != "assistant" {
		return "", "", false
	}
	text := strings.TrimSpace(portableText(message["content"]))
	if text == "" {
		return "", "", false
	}
	return role, text, true
}

func portableConversationTurn(line []byte) (string, string, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return "", "", false
	}
	var record map[string]json.RawMessage
	if json.Unmarshal(line, &record) != nil {
		return "", "", false
	}
	candidate := record
	if raw, ok := record["message"]; ok {
		var message map[string]json.RawMessage
		if json.Unmarshal(raw, &message) == nil {
			candidate = message
		}
	} else if raw, ok := record["payload"]; ok {
		var payload map[string]json.RawMessage
		if json.Unmarshal(raw, &payload) == nil {
			// Codex-style records put their conversation item below payload.
			candidate = payload
		}
	}

	role := portableString(candidate["role"])
	if role == "" {
		role = portableString(record["type"])
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "user" && role != "assistant" {
		return "", "", false
	}
	text := strings.TrimSpace(portableText(candidate["content"]))
	if text == "" {
		// Some rollout formats use a single text field for an otherwise normal
		// user/assistant message. Do not recurse through arbitrary objects.
		text = strings.TrimSpace(portableString(candidate["text"]))
	}
	if text == "" {
		return "", "", false
	}
	return role, text, true
}

func portableString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func portableText(raw json.RawMessage) string {
	if text := portableString(raw); text != "" {
		return text
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		typ := strings.ToLower(strings.TrimSpace(portableString(block["type"])))
		switch typ {
		case "text", "input_text", "output_text":
			if text := strings.TrimSpace(portableString(block["text"])); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
