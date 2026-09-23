package tmux

import (
	"regexp"
	"strconv"
	"strings"
)

// PaneStatus is what the terminal shows about a running turn that no
// transcript carries: the spinner verb and its elapsed/tokens, the current
// tool line under it, queued inputs, the footer and mode badges. It is
// parsed from a read-only capture-pane so clients never read tmux.
type PaneStatus struct {
	Running     bool   `json:"running"`
	Verb        string `json:"verb,omitempty"`
	Elapsed     string `json:"elapsed,omitempty"`
	Tokens      string `json:"tokens,omitempty"`
	CurrentTool string `json:"current_tool,omitempty"`
	Queued      int    `json:"queued,omitempty"`
	// QueuedText holds the queued inputs the pane lists (Codex "↳" lines).
	QueuedText []string `json:"queued_text,omitempty"`
	Footer     string   `json:"footer,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	Notice     string   `json:"notice,omitempty"`
	// AutoCompactPct is Claude's "N% until auto-compact", nil when absent.
	AutoCompactPct *int `json:"auto_compact_pct,omitempty"`
}

var (
	// Claude Code: "✢ Symbioting… (1m 34s · ↓ 3.7k tokens)".
	claudeSpinnerStatusRe = regexp.MustCompile(`^\s*[✻✢✽∗·✶✳*✺✹]\s+([^\s(][^(]*?…)\s*(?:\(([^)]*)\))?\s*$`)
	// Codex: "• Working (30m 26s • esc to interrupt) · 1 background terminal running".
	codexWorkingStatusRe = regexp.MustCompile(`^\s*•\s+(\S[^(]*?)\s+\(((?:\d+[hms]\s*)+)[•·]\s*esc to interrupt\)(.*)$`)
	elapsedPartRe        = regexp.MustCompile(`^(?:\d+h\s*)?(?:\d+m\s*)?\d+s$`)
	autoCompactRe        = regexp.MustCompile(`(\d+)% until auto-compact`)
	elapsedUnitRe        = regexp.MustCompile(`(\d+)([hms])`)
)

// ElapsedSeconds converts "1h 2m 3s" / "1m 34s" / "12s" to seconds.
func ElapsedSeconds(s string) int {
	total := 0
	for _, m := range elapsedUnitRe.FindAllStringSubmatch(s, -1) {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "h":
			total += n * 3600
		case "m":
			total += n * 60
		default:
			total += n
		}
	}
	return total
}

// FooterFacts splits a statusline into named facts. Claude:
// "[personal] user@host:/path | [Fable 5.1] ctx:5% in:52.4k out:376 5h:40% 7d:20%"
// gives account, cwd, model, ctx, in, out, 5h, 7d. Codex:
// "gpt-6-sol · /path · Context 50% left · weekly 87% left · 258K window"
// gives model, cwd, context_left, context_used, weekly_left, window.
func FooterFacts(footer string) map[string]string {
	facts := map[string]string{}
	footer = strings.TrimSpace(footer)
	if footer == "" {
		return facts
	}
	if strings.HasPrefix(footer, "[") {
		left, right, _ := strings.Cut(footer, " | ")
		if i := strings.Index(left, "]"); i > 0 {
			facts["account"] = left[1:i]
			if _, cwd, ok := strings.Cut(left[i+1:], ":"); ok {
				facts["cwd"] = strings.TrimSpace(cwd)
			}
		}
		if strings.HasPrefix(right, "[") {
			if i := strings.Index(right, "]"); i > 0 {
				facts["model"] = right[1:i]
				right = right[i+1:]
			}
		}
		for _, f := range strings.Fields(right) {
			if k, v, ok := strings.Cut(f, ":"); ok && k != "" && v != "" {
				facts[k] = v
			}
		}
		return facts
	}
	for i, part := range strings.Split(footer, " · ") {
		part = strings.TrimSpace(part)
		switch {
		case i == 0:
			facts["model"] = part
		case strings.HasPrefix(part, "/") || strings.HasPrefix(part, "~"):
			facts["cwd"] = part
		case strings.HasPrefix(part, "Context ") && strings.HasSuffix(part, " left"):
			facts["context_left"] = strings.TrimSuffix(strings.TrimPrefix(part, "Context "), " left")
		case strings.HasPrefix(part, "Context ") && strings.HasSuffix(part, " used"):
			facts["context_used"] = strings.TrimSuffix(strings.TrimPrefix(part, "Context "), " used")
		case strings.HasPrefix(part, "weekly ") && strings.HasSuffix(part, " left"):
			facts["weekly_left"] = strings.TrimSuffix(strings.TrimPrefix(part, "weekly "), " left")
		case strings.HasSuffix(part, " window"):
			facts["window"] = strings.TrimSuffix(part, " window")
		}
	}
	return facts
}

// ParsePaneStatus reads the visible pane of a Claude Code or Codex session.
func ParsePaneStatus(content string) PaneStatus {
	var st PaneStatus
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	spinnerAt := -1
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], " ")
		if m := claudeSpinnerStatusRe.FindStringSubmatch(line); m != nil && spinnerAt < 0 && !strings.Contains(line, " for ") {
			st.Running, st.Verb, spinnerAt = true, strings.TrimSpace(m[1]), i
			for _, part := range strings.Split(m[2], "·") {
				part = strings.TrimSpace(part)
				switch {
				case elapsedPartRe.MatchString(part):
					st.Elapsed = part
				case strings.Contains(part, "tokens"):
					st.Tokens = part
				}
			}
			continue
		}
		if m := codexWorkingStatusRe.FindStringSubmatch(line); m != nil && spinnerAt < 0 {
			st.Running, st.Verb, st.Elapsed, spinnerAt = true, strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), i
			if extra := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m[3]), "·")); extra != "" {
				st.CurrentTool = extra
			}
		}
	}
	if spinnerAt >= 0 && spinnerAt+1 < len(lines) && st.CurrentTool == "" {
		next := strings.TrimSpace(lines[spinnerAt+1])
		if strings.HasPrefix(next, "⎿") {
			next = strings.TrimSpace(strings.TrimPrefix(next, "⎿"))
			if !strings.HasPrefix(next, "Tip:") {
				st.CurrentTool = next
			}
		}
	}
	inQueue := false
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "• Queued follow-up inputs"):
			inQueue = true
			continue
		case inQueue && strings.HasPrefix(line, "↳"):
			st.Queued++
			st.QueuedText = append(st.QueuedText, strings.TrimSpace(strings.TrimPrefix(line, "↳")))
			continue
		case strings.Contains(line, "Press up to edit queued messages"):
			if st.Queued == 0 {
				st.Queued = 1
			}
		case strings.HasPrefix(line, "⏵⏵") || strings.HasPrefix(line, "⏸"):
			mode := strings.TrimSpace(strings.TrimLeft(line, "⏵⏸ "))
			if j := strings.Index(mode, " ("); j >= 0 {
				mode = mode[:j]
			}
			if j := strings.Index(mode, " · "); j >= 0 {
				mode = mode[:j]
			}
			st.Mode = mode
		case strings.HasPrefix(line, "✘ ") || strings.Contains(line, " ✘ "):
			st.Notice = strings.TrimSpace(line[strings.Index(line, "✘"):])
		case isFooterLine(line, i, lines):
			st.Footer = line
		}
		if m := autoCompactRe.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			st.AutoCompactPct = &n
		}
		inQueue = inQueue && strings.HasPrefix(line, "↳")
	}
	return st
}

// isFooterLine recognizes the statusline under the composer: Claude's
// "[profile] user@host:/path | [model] ctx:5% ..." and Codex's
// "model · /path · Context 90% left · weekly 87% left".
func isFooterLine(line string, i int, lines []string) bool {
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "[") && strings.Contains(line, "@") && strings.Contains(line, ":") {
		return true
	}
	if strings.Contains(line, " · ") && (strings.Contains(line, "% left") || strings.Contains(line, "% used")) {
		return i > 0 && strings.HasPrefix(strings.TrimSpace(lines[i-1]), "›")
	}
	return false
}
