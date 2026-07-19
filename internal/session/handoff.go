package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const DefaultHandoffMaxChars = 32000

// HandoffInfo describes the source transcript used to build a tool handoff.
type HandoffInfo struct {
	TranscriptPath string `json:"transcript_path"`
	MessageCount   int    `json:"message_count"`
	IncludedCount  int    `json:"included_count"`
	Truncated      bool   `json:"truncated"`
	MaxChars       int    `json:"max_chars"`
}

type handoffMessage struct {
	Role    string
	Content string
}

// BuildClaudeToCodexHandoffPrompt builds a prompt that carries a Claude
// transcript into a fresh Codex session. This intentionally does not try to
// write Codex's private rollout JSONL format; the stable cross-tool contract is
// a plain initial prompt containing the prior conversation tail.
func BuildClaudeToCodexHandoffPrompt(inst *Instance, maxChars int) (string, HandoffInfo, error) {
	if inst == nil {
		return "", HandoffInfo{}, fmt.Errorf("session is nil")
	}
	if maxChars <= 0 {
		maxChars = DefaultHandoffMaxChars
	}
	if inst.ClaudeSessionID == "" {
		return "", HandoffInfo{}, fmt.Errorf("session %q has no Claude session ID", inst.Title)
	}

	transcriptPath := locateHandoffTranscript(inst)
	messages, err := readClaudeTranscriptMessages(transcriptPath)
	if err != nil {
		return "", HandoffInfo{TranscriptPath: transcriptPath, MaxChars: maxChars}, err
	}
	if len(messages) == 0 {
		return "", HandoffInfo{TranscriptPath: transcriptPath, MaxChars: maxChars}, fmt.Errorf("Claude transcript has no readable messages: %s", transcriptPath)
	}

	included, truncated := tailMessagesByChars(messages, maxChars)
	var body strings.Builder
	for idx, msg := range included {
		if idx > 0 {
			body.WriteString("\n\n")
		}
		body.WriteString("[")
		body.WriteString(strings.ToUpper(msg.Role))
		body.WriteString("]\n")
		body.WriteString(msg.Content)
	}

	prompt := fmt.Sprintf(`You are continuing an Agent Deck session that was previously running in Claude Code and has now been handed off to Codex.

Original session:
- title: %s
- project: %s
- previous tool: %s
- previous Claude session ID: %s

The transcript below is prior conversation context. Treat it as conversation history for this handoff and continue from it. Do not claim you cannot access prior messages; this handoff prompt is the transferred history.

--- BEGIN TRANSFERRED TRANSCRIPT ---
%s
--- END TRANSFERRED TRANSCRIPT ---

This message is context initialization only. Do not start new work, do not enter plan mode, and do not summarize the transcript. Reply exactly: HANDOFF RECEIVED.`, inst.Title, inst.ProjectPath, inst.Tool, inst.ClaudeSessionID, body.String())

	return prompt, HandoffInfo{
		TranscriptPath: transcriptPath,
		MessageCount:   len(messages),
		IncludedCount:  len(included),
		Truncated:      truncated,
		MaxChars:       maxChars,
	}, nil
}

// ClaudeTranscriptPathForInstance returns the expected JSONL transcript path
// for the instance's Claude session ID, using the same cwd encoding as the
// rest of the codebase (issue #663: multi-repo sessions log under
// EffectiveWorkingDir, and only then may symlinks be resolved).
func ClaudeTranscriptPathForInstance(inst *Instance) string {
	if inst == nil || inst.ClaudeSessionID == "" {
		return ""
	}
	return claudeTranscriptPathIn(GetClaudeConfigDirForInstance(inst), inst, inst.ClaudeSessionID)
}

func claudeTranscriptPathIn(configDir string, inst *Instance, sessionID string) string {
	projectPath := inst.EffectiveWorkingDir()
	if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
		projectPath = resolved
	}
	encoded := ConvertToClaudeDirName(projectPath)
	if encoded == "" {
		encoded = "-"
	}
	return filepath.Join(configDir, "projects", encoded, sessionID+".jsonl")
}

// locateHandoffTranscript picks the transcript to hand off. The disk is
// authoritative: account-switched or pre-account sessions may keep their
// conversation in a different config dir than the resolver's answer, so scan
// all configured dirs (issue #1571 machinery) and fall back to the resolver
// path when nothing is found.
func locateHandoffTranscript(inst *Instance) string {
	fallback := ClaudeTranscriptPathForInstance(inst)
	cfg, err := LoadUserConfig()
	if err != nil {
		return fallback
	}
	dir, sid, _ := LocateConversationConfigDir(cfg, inst, GetClaudeConfigDirForInstance(inst))
	if dir == "" {
		return fallback
	}
	if sid == "" {
		sid = inst.ClaudeSessionID
	}
	// LocateConversationConfigDir matches on the raw ProjectPath encoding;
	// prefer the canonical encoding when it exists in the located dir.
	canonical := claudeTranscriptPathIn(dir, inst, sid)
	if _, statErr := os.Stat(canonical); statErr == nil {
		return canonical
	}
	raw := filepath.Join(dir, "projects", ConvertToClaudeDirName(inst.ProjectPath), sid+".jsonl")
	if _, statErr := os.Stat(raw); statErr == nil {
		return raw
	}
	return fallback
}

func readClaudeTranscriptMessages(path string) ([]handoffMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read Claude transcript: %w", err)
	}
	defer f.Close()

	// ReadString instead of a fixed-buffer Scanner: transcripts can contain
	// multi-megabyte records (file-history snapshots, compaction), and a
	// single oversized line must not abort the whole handoff.
	reader := bufio.NewReaderSize(f, 64*1024)
	var messages []handoffMessage
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			if msg, ok := parseClaudeTranscriptLine([]byte(line)); ok {
				messages = append(messages, msg)
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("scan Claude transcript: %w", err)
		}
	}
	return messages, nil
}

func parseClaudeTranscriptLine(line []byte) (handoffMessage, bool) {
	var raw struct {
		Type        string `json:"type"`
		IsSidechain bool   `json:"isSidechain"`
		Message     struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return handoffMessage{}, false
	}
	if raw.IsSidechain {
		// Subagent sidechain records are not main-conversation turns.
		return handoffMessage{}, false
	}

	role := strings.TrimSpace(raw.Message.Role)
	if role == "" {
		role = strings.TrimSpace(raw.Type)
	}
	switch role {
	case "user", "assistant", "system":
	default:
		return handoffMessage{}, false
	}

	content := strings.TrimSpace(renderClaudeContent(raw.Message.Content))
	if content == "" {
		return handoffMessage{}, false
	}
	return handoffMessage{Role: role, Content: content}, true
}

func renderClaudeContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}

	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err == nil {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			part := renderClaudeContentBlock(block)
			if part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, "\n")
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		return compactJSON(raw)
	}
	return string(raw)
}

func renderClaudeContentBlock(block map[string]json.RawMessage) string {
	var typ string
	_ = json.Unmarshal(block["type"], &typ)

	switch typ {
	case "text":
		var text string
		_ = json.Unmarshal(block["text"], &text)
		return strings.TrimSpace(text)
	case "thinking":
		return ""
	case "tool_use":
		var name string
		_ = json.Unmarshal(block["name"], &name)
		input := compactJSON(block["input"])
		if input == "" {
			return fmt.Sprintf("[tool_use %s]", name)
		}
		return fmt.Sprintf("[tool_use %s] %s", name, input)
	case "tool_result":
		content := renderClaudeContent(block["content"])
		if content == "" {
			return "[tool_result]"
		}
		return "[tool_result]\n" + content
	default:
		if typ != "" {
			return "[" + typ + "] " + compactJSONMust(block)
		}
		return compactJSONMust(block)
	}
}

func tailMessagesByChars(messages []handoffMessage, maxChars int) ([]handoffMessage, bool) {
	if maxChars <= 0 || len(messages) == 0 {
		return messages, false
	}
	total := 0
	start := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		cost := len(messages[i].Role) + len(messages[i].Content) + 8
		if total+cost > maxChars {
			if total == 0 {
				// Even the newest message alone exceeds the budget: keep its
				// tail (most recent content) so maxChars is a real ceiling.
				trimmed := messages[i]
				keep := maxChars - len(trimmed.Role) - 8
				if keep < 0 {
					keep = 0
				}
				if keep < len(trimmed.Content) {
					trimmed.Content = "[…earlier content truncated…]\n" +
						strings.ToValidUTF8(trimmed.Content[len(trimmed.Content)-keep:], "")
				}
				return []handoffMessage{trimmed}, true
			}
			break
		}
		total += cost
		start = i
	}
	return messages[start:], start > 0
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func compactJSONMust(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return compactJSON(raw)
}
