package tmux

import "strings"

// piBusyPatternContent blanks complete fenced examples while preserving line
// positions in the original 25-line tail window. An unmatched fence is left intact:
// a streaming answer must not hide the live interrupt bar beneath it.
func piBusyPatternContent(content string) string {
	if !strings.Contains(content, "```") && !strings.Contains(content, "~~~") {
		return strings.Join(lastNLines(content, 25), "\n")
	}
	lines := lastNLines(content, strings.Count(content, "\n")+1)
	start, fence := -1, ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if start >= 0 {
			if strings.TrimRight(trimmed, fence[:1]) == "" && len(trimmed) >= len(fence) {
				for j := start; j <= i; j++ {
					lines[j] = ""
				}
				start, fence = -1, ""
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "```") && !strings.HasPrefix(trimmed, "~~~") {
			continue
		}
		n := 0
		for n < len(trimmed) && trimmed[n] == trimmed[0] {
			n++
		}
		if trimmed[0] == '`' && strings.Contains(trimmed[n:], "`") {
			continue
		}
		start, fence = i, trimmed[:n]
	}
	return strings.Join(lines[max(0, len(lines)-25):], "\n")
}
