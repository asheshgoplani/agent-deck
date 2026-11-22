// Package shellwords provides the small subset of shell tokenization needed
// when inspecting command lines without evaluating them.
package shellwords

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Split separates a command line into words while honoring single quotes,
// double quotes, and backslash escapes; inside double quotes a backslash only
// escapes $, `, ", \ and newline, as in POSIX sh. It deliberately performs no
// expansion (variables, globs, command substitutions, or operators). The
// boolean is false for an unterminated quote or trailing escape.
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
			escaped = false
			inWord = true
			if quote == '"' {
				// Inside double quotes a backslash is special only before
				// $ ` " \ and newline; an escaped newline is removed.
				switch r {
				case '\n':
					continue
				case '$', '`', '"', '\\':
				default:
					word.WriteRune('\\')
				}
			}
			word.WriteRune(r)
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

// CommandSpan is one simple command's text and its byte range in the original
// line. Start is inclusive, End exclusive, and both exclude surrounding
// unquoted blanks, so callers can rewrite that command in place without
// matching text inside a comment or a later command.
type CommandSpan struct {
	Text  string
	Start int
	End   int
}

// Commands separates a command line into its simple commands at unquoted
// control operators (&&, ||, ;, |, and newlines), keeping each command's text,
// quotes included, so it can be passed to Split. An unquoted # that starts a
// word begins a shell comment through the end of that line; quote characters
// inside it are ordinary text, and the newline still separates commands. Like
// Split it performs no expansion, and the boolean is false for an unterminated
// quote or trailing escape.
func Commands(command string) ([]string, bool) {
	spans, ok := CommandSpans(command)
	if !ok {
		return nil, false
	}
	var commands []string
	for _, span := range spans {
		commands = append(commands, span.Text)
	}
	return commands, true
}

// CommandSpans is Commands with each command's byte range, for callers that
// rewrite the original line rather than the extracted text.
func CommandSpans(command string) ([]CommandSpan, bool) {
	var spans []CommandSpan
	start, end := -1, -1
	var quote rune
	escaped := false
	inComment := false
	skipNext := false

	flush := func() {
		if start >= 0 {
			spans = append(spans, CommandSpan{Text: command[start:end], Start: start, End: end})
		}
		start, end = -1, -1
	}

	// atWordStart tracks shell lexical state rather than the previous rune:
	// an escaped or quoted space belongs to the word, so a # after it is data,
	// not a comment.
	atWordStart := true
	for i, r := range command {
		if skipNext {
			skipNext = false
			continue
		}
		size := utf8.RuneLen(r)
		if size < 1 {
			size = 1
		}
		if inComment {
			if r == '\n' {
				inComment = false
				flush()
				atWordStart = true
			}
			continue
		}
		data := true
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
		case r == '#' && atWordStart:
			// An unquoted # that starts a word is a comment, so an apostrophe
			// in it cannot open a quote that never closes.
			inComment = true
			continue
		case r == ';' || r == '\n':
			flush()
			atWordStart = true
			continue
		case r == '|':
			flush()
			atWordStart = true
			if i+1 < len(command) && command[i+1] == '|' {
				skipNext = true
			}
			continue
		case r == '&' && i+1 < len(command) && command[i+1] == '&':
			flush()
			atWordStart = true
			skipNext = true
			continue
		case r == ' ' || r == '\t':
			atWordStart = true
			data = false
		case r == '&' || r == '(' || r == ')':
			// Word separators to the shell, though the command keeps them.
			atWordStart = true
		default:
			atWordStart = false
		}
		if !data {
			continue
		}
		if start < 0 {
			start = i
		}
		end = i + size
	}
	if escaped || quote != 0 {
		return nil, false
	}
	flush()
	return spans, true
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
			// env has no -c or -s of its own, so -C DIR and -S STR are matched in
			// either case: a caller that lowercases the command line first
			// (detectToolFromCommand) still skips their values.
			i = afterOptions(words, i+1, "uCScs", envLongOptionsWithValue)
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
