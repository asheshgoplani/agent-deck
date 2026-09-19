package recall

import "strings"

// Turn is one message as a handoff renders it: a role and its decoded
// text. It is harness-neutral on purpose: a Claude excerpt handed to a
// Codex session and a Codex excerpt handed to Claude render the same way.
type Turn struct {
	Role    string
	Content string
}

// TruncatedMarker opens a turn whose head was cut to fit the budget.
const TruncatedMarker = "[…earlier content truncated…]\n"

// TailByChars keeps the newest turns that fit in maxChars (each turn
// costing its role, its content and a small frame), and reports whether
// anything was left out. When even the newest turn alone is over budget,
// its tail is kept so maxChars is a real ceiling. maxChars <= 0 keeps
// everything. This is the handoff renderer's rule, shared by
// `session handoff` and `recall context`.
func TailByChars(turns []Turn, maxChars int) ([]Turn, bool) {
	if maxChars <= 0 || len(turns) == 0 {
		return turns, false
	}
	total := 0
	start := len(turns)
	for i := len(turns) - 1; i >= 0; i-- {
		cost := len(turns[i].Role) + len(turns[i].Content) + 8
		if total+cost > maxChars {
			if total == 0 {
				trimmed := turns[i]
				keep := maxChars - len(trimmed.Role) - 8
				if keep < 0 {
					keep = 0
				}
				if keep < len(trimmed.Content) {
					trimmed.Content = TruncatedMarker + strings.ToValidUTF8(trimmed.Content[len(trimmed.Content)-keep:], "")
				}
				return []Turn{trimmed}, true
			}
			break
		}
		total += cost
		start = i
	}
	return turns[start:], start > 0
}

// RenderTurns writes turns as "[ROLE]\ncontent" blocks separated by blank
// lines: the plain-text contract every harness can take as a prompt.
func RenderTurns(turns []Turn) string {
	var body strings.Builder
	for idx, t := range turns {
		if idx > 0 {
			body.WriteString("\n\n")
		}
		body.WriteString("[")
		body.WriteString(strings.ToUpper(t.Role))
		body.WriteString("]\n")
		body.WriteString(t.Content)
	}
	return body.String()
}
