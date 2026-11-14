package logging

import "regexp"

// RedactedEnvValue replaces every environment value RedactCommandEnv hides.
const RedactedEnvValue = "***"

// envAssignmentPattern matches a shell environment assignment that starts a
// shell word: `export KEY=value`, a bare `KEY=value` command prefix, or a tmux
// `-e KEY=value` argument. The value is a run of adjacent quoted / escaped /
// bare segments, so the backslash-quote escape of the env builders, the
// `'"'"'` escaping used when a command is re-wrapped in `bash -c '…'`, and an unterminated trailing quote are all
// consumed as one value. When the quoting is ambiguous the pattern errs on the
// side of hiding MORE of the line, never less.
var envAssignmentPattern = regexp.MustCompile(
	`(^|[\s;&|()'"])((?:export[ \t]+)?[A-Za-z_][A-Za-z0-9_]*=)` +
		`((?:'[^']*'?|"(?:\\.|[^"\\])*"?|\\.|[^\s;&|()'"\\]+)*)`)

// RedactCommandEnv returns s with the value of every shell environment
// assignment replaced by RedactedEnvValue while keeping the variable name, so
// a logged launch/restart command still shows WHICH variables were set but
// never their values (LOC-4: inline env from config can carry API keys).
// It also applies SanitizeValue, so the result is safe to hand to a log sink.
func RedactCommandEnv(s string) string {
	redacted := envAssignmentPattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := envAssignmentPattern.FindStringSubmatch(match)
		if len(parts) != 4 || parts[3] == "" {
			return match
		}
		return parts[1] + parts[2] + RedactedEnvValue
	})
	return SanitizeValue(redacted)
}

// RedactCommandEnvArgs applies RedactCommandEnv to every element of an argv
// slice (e.g. a tmux new-session / respawn-pane argument list) and returns a
// new slice.
func RedactCommandEnvArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = RedactCommandEnv(a)
	}
	return out
}
