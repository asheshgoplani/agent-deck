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

// CommandWords returns the words from the command-position word on. It skips
// environment assignments and the prefixes that run the program after them,
// together with their options: exec (-a NAME), env (-i, -u NAME, --unset NAME,
// -C DIR, ...), command (-p), nice (-n N, -N, --adjustment N), and sudo. So
// `exec env -u NAME codex --yolo` yields [codex --yolo]. It returns nil when no
// command word remains.
func CommandWords(words []string) []string {
	for i := 0; i < len(words); {
		switch word := words[i]; {
		case isAssignment(word):
			i++
		case word == "exec":
			i = afterOptions(words, i+1, "a", nil)
		case word == "env":
			i = afterOptions(words, i+1, "uCS", envLongOptionsWithValue)
		case word == "command":
			i = afterOptions(words, i+1, "", nil)
		case word == "nice":
			i = afterOptions(words, i+1, "n", niceLongOptionsWithValue)
		case word == "sudo":
			i++
		default:
			return words[i:]
		}
	}
	return nil
}

// envLongOptionsWithValue are env's long options whose value may be the next
// word ("--unset NAME") rather than attached ("--unset=NAME").
var envLongOptionsWithValue = map[string]bool{"--unset": true, "--chdir": true, "--split-string": true}

// niceLongOptionsWithValue is nice's long option whose value may be the next
// word ("--adjustment 10") rather than attached ("--adjustment=10").
var niceLongOptionsWithValue = map[string]bool{"--adjustment": true}

// afterOptions returns the index of the first word after the options that
// start at words[i]. A short option ending in one of shortWithValue, or a long
// option in longWithValue, takes the next word as its value. "--" ends the
// options, and a lone "-" (env's spelling of -i) is itself an option.
func afterOptions(words []string, i int, shortWithValue string, longWithValue map[string]bool) int {
	for i < len(words) {
		word := words[i]
		switch {
		case word == "--":
			return i + 1
		case word == "-":
			i++
		case strings.HasPrefix(word, "--"):
			i++
			if longWithValue[word] {
				i++
			}
		case strings.HasPrefix(word, "-"):
			i++
			if strings.ContainsRune(shortWithValue, rune(word[len(word)-1])) {
				i++
			}
		default:
			return i
		}
	}
	return i
}

// ExecutableBase returns the basename of the command-position word that
// CommandWords finds. A trailing slash denotes a directory and is not treated
// as an executable.
func ExecutableBase(words []string) string {
	words = CommandWords(words)
	if len(words) == 0 || strings.HasSuffix(words[0], "/") {
		return ""
	}
	return filepath.Base(words[0])
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
