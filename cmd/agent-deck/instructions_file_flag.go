package main

import (
	"flag"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// instructionsFileUsage is the --instructions-file help text on add and launch.
const instructionsFileUsage = "Instruction file appended to this session's identity block at every start, restart, resume and fork (repeatable, or comma-separated; absolute path, ~ and $VAR expanded; #3206)"

// repeatableStringsFlag is a repeatable string flag that says so in
// `add|launch --capabilities --json` (RemoteCreationField.Repeatable), so a
// script can detect a build that accepts it more than once.
type repeatableStringsFlag struct{ values *[]string }

func (f repeatableStringsFlag) String() string {
	if f.values == nil {
		return ""
	}
	return strings.Join(*f.values, ",")
}

func (f repeatableStringsFlag) Set(s string) error {
	*f.values = append(*f.values, s)
	return nil
}

// IsRepeatable marks the flag for creationFieldsFromFlags.
func (repeatableStringsFlag) IsRepeatable() bool { return true }

// registerInstructionsFileFlag adds --instructions-file to a creation flag set.
func registerInstructionsFileFlag(fs *flag.FlagSet) *[]string {
	var values []string
	fs.Var(repeatableStringsFlag{values: &values}, "instructions-file", instructionsFileUsage)
	return &values
}

// normalizeInstructionsFileFlag validates the collected flag values; nil
// when the flag was not given.
func normalizeInstructionsFileFlag(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	return session.NormalizeInstructionsFiles(values)
}
