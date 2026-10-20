// Package shellwords provides the small subset of shell tokenization needed
// when inspecting command lines without evaluating them.
package shellwords

import (
	"path/filepath"
	"strings"
	"unicode"
)

// Split separates a command line into words while honoring single quotes,
// double quotes, and backslash escapes. It deliberately performs no expansion
// (variables, globs, command substitutions, or operators). The boolean is false
// for an unterminated quote or trailing escape.
func Split(command string) ([]string, bool) {
	var words []string
	var word strings.Builder
	inWord := false
	var quote rune
	escaped := false

	flush := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}

	for _, r := range command {
		if escaped {
			word.WriteRune(r)
			inWord = true
			escaped = false
			continue
		}
		if quote == '\'' {
			if r == '\'' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			inWord = true
			continue
		}
		if r == '\\' {
			escaped = true
			inWord = true
			continue
		}
		if quote == '"' {
			if r == '"' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			inWord = true
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case unicode.IsSpace(r):
			flush()
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if escaped || quote != 0 {
		return nil, false
	}
	flush()
	return words, true
}

// Commands separates a command line into its simple commands at unquoted
// control operators (&&, ||, ;, |, and newlines), keeping each command's text,
// quotes included, so it can be passed to Split. Like Split it performs no
// expansion, and the boolean is false for an unterminated quote or trailing
// escape.
func Commands(command string) ([]string, bool) {
	var commands []string
	var current strings.Builder
	var quote rune
	escaped := false

	flush := func() {
		if text := strings.TrimSpace(current.String()); text != "" {
			commands = append(commands, text)
		}
		current.Reset()
	}

	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case escaped:
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			}
		case r == '\\':
			escaped = true
		case quote == '"':
			if r == '"' {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ';' || r == '\n':
			flush()
			continue
		case r == '|':
			flush()
			if i+1 < len(runes) && runes[i+1] == '|' {
				i++
			}
			continue
		case r == '&' && i+1 < len(runes) && runes[i+1] == '&':
			flush()
			i++
			continue
		}
		current.WriteRune(r)
	}
	if escaped || quote != 0 {
		return nil, false
	}
	flush()
	return commands, true
}

// ExecutableBase returns the basename of the command-position word. Leading
// env/sudo wrappers and environment assignments are skipped. A trailing slash
// denotes a directory and is not treated as an executable.
func ExecutableBase(words []string) string {
	for _, word := range words {
		if word == "env" || word == "sudo" || isAssignment(word) {
			continue
		}
		if strings.HasSuffix(word, "/") {
			return ""
		}
		return filepath.Base(word)
	}
	return ""
}

func isAssignment(word string) bool {
	eq := strings.IndexByte(word, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range word[:eq] {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
