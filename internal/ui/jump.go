package ui

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// hintCharacters defines the character set for jump hints.
// Uses home row keys (Vimium default) for ergonomic typing.
const hintCharacters = "sadfjklewcmpgh"

// generateJumpHints creates hint labels for n items using Vimium's algorithm.
// Maximizes single-key hints: with 14 chars, up to 13 items need only 1 keypress.
// Two-key hints are interleaved with single-key ones for even distribution.
func generateJumpHints(count int) []string {
	if count == 0 {
		return nil
	}

	// Vimium's tree expansion: iteratively expand hints by prepending each character
	hints := []string{""}
	offset := 0
	for (len(hints)-offset) < count || len(hints) == 1 {
		hint := hints[offset]
		offset++
		for _, ch := range hintCharacters {
			hints = append(hints, string(ch)+hint)
		}
	}

	// Slice to exact count, sort, then reverse each string.
	// Sorting distributes two-char hints between single-char ones.
	// Reversing converts prepended prefixes into typed prefixes (e.g., "as" → "sa").
	result := make([]string, count)
	copy(result, hints[offset:offset+count])
	sort.Strings(result)
	for i, s := range result {
		result[i] = reverseString(s)
	}
	return result
}

func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

type jumpMatchResult struct {
	matched  bool
	index    int
	isPrefix bool
}

// matchJumpHint checks the buffer against the hint list.
func matchJumpHint(hints []string, buffer string) jumpMatchResult {
	if buffer == "" {
		return jumpMatchResult{matched: false, index: -1, isPrefix: true}
	}

	exactIndex := -1
	prefixCount := 0

	for i, h := range hints {
		if h == buffer {
			exactIndex = i
			prefixCount++
		} else if strings.HasPrefix(h, buffer) {
			prefixCount++
		}
	}

	if exactIndex >= 0 && prefixCount == 1 {
		return jumpMatchResult{matched: true, index: exactIndex, isPrefix: false}
	}
	if prefixCount > 0 {
		return jumpMatchResult{matched: false, index: -1, isPrefix: true}
	}
	return jumpMatchResult{matched: false, index: -1, isPrefix: false}
}

// ansiRegex matches CSI sequences, including private-mode ones such as the
// per-row DECAWM toggles clampViewToViewport emits (\x1b[?7l / \x1b[?7h).
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func stripAnsi(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// skipVisibleChars advances through s starting at byteIdx, skipping n visible characters
// while respecting ANSI escape sequences. Returns the new byte index.
func skipVisibleChars(s string, byteIdx int, n int) int {
	if n == 0 {
		return byteIdx
	}

	visible := 0
	inEscape := false

	for byteIdx < len(s) && visible < n {
		if s[byteIdx] == '\x1b' {
			inEscape = true
			byteIdx++
			continue
		}
		if inEscape {
			if (s[byteIdx] >= 'A' && s[byteIdx] <= 'Z') || (s[byteIdx] >= 'a' && s[byteIdx] <= 'z') {
				inEscape = false
			}
			byteIdx++
			continue
		}
		_, size := utf8.DecodeRuneInString(s[byteIdx:])
		visible++
		byteIdx += size
	}

	// Skip any trailing ANSI sequences at the cut point
	for byteIdx < len(s) && s[byteIdx] == '\x1b' {
		byteIdx++
		for byteIdx < len(s) {
			ch := s[byteIdx]
			byteIdx++
			if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
				break
			}
		}
	}

	return byteIdx
}

// replaceVisibleRange replaces n visible characters starting at visible position 'start'
// in an ANSI-styled string with the given replacement.
func replaceVisibleRange(s string, start, n int, replacement string) string {
	startByte := skipVisibleChars(s, 0, start)
	endByte := skipVisibleChars(s, startByte, n)
	return s[:startByte] + replacement + s[endByte:]
}

// findNameOffset returns the visible character offset where 'name' starts in the
// ANSI-stripped version of s. Returns 0 if not found.
func findNameOffset(s string, name string) int {
	if name == "" {
		return 0
	}
	stripped := stripAnsi(s)
	idx := strings.Index(stripped, name)
	if idx < 0 {
		return 0
	}
	return utf8.RuneCountInString(stripped[:idx])
}
