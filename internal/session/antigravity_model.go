package session

import (
	"fmt"
	"strings"
	"unicode"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/shellwords"
)

// applyAntigravityModel sets agy's --model on the session's launch command.
// The flag lives in the command itself, so start, restart (resume) and fork,
// which all rebuild from the command and keep --model, launch the same model.
// The value is shell-quoted, never interpreted. Use the IDs `agy models`
// prints (for example gemini-4-hydrogen-high or gemini-pro-orchestrator-neon):
// display names such as "Orchestrator (Neon)" contain characters the
// restart and fork rewriters refuse, so they are rejected here up front.
func (i *Instance) applyAntigravityModel(model string) error {
	if err := validateAntigravityModel(model); err != nil {
		return err
	}
	base, err := antigravityCommandWithoutModel(i.antigravityCommand())
	if err != nil {
		return err
	}
	i.Command = base + " --model " + shellescape.Quote(model)
	return nil
}

// clearAntigravityModel removes --model from the launch command, so agy falls
// back to the model chosen in its own /model menu.
func (i *Instance) clearAntigravityModel() error {
	if strings.TrimSpace(i.Command) == "" {
		return nil
	}
	base, err := antigravityCommandWithoutModel(i.antigravityCommand())
	if err != nil {
		return err
	}
	i.Command = base
	return nil
}

// AntigravityLaunchModel returns the --model value of the session's launch
// command, or "" when none is set.
func (i *Instance) AntigravityLaunchModel() string {
	if i == nil || i.Tool != "antigravity" {
		return ""
	}
	tokens, ok := shellwords.Split(strings.TrimSpace(i.antigravityCommand()))
	if !ok {
		return ""
	}
	model := ""
	for idx := 0; idx < len(tokens); idx++ {
		switch tok := tokens[idx]; {
		case tok == "--model" && idx+1 < len(tokens):
			model = tokens[idx+1]
			idx++
		case strings.HasPrefix(tok, "--model="):
			model = strings.TrimPrefix(tok, "--model=")
		}
	}
	return model
}

func validateAntigravityModel(model string) error {
	if model == "" || len(model) > 128 {
		return fmt.Errorf("antigravity model must be 1 to 128 characters")
	}
	if strings.HasPrefix(model, "-") {
		return fmt.Errorf("antigravity model %q must not start with '-'", model)
	}
	for _, r := range model {
		if unicode.IsControl(r) {
			return fmt.Errorf("antigravity model must not contain control characters")
		}
	}
	if antigravityTokenNeedsShell(model, true) {
		return fmt.Errorf("antigravity model %q has shell characters; use the model ID from `agy models` (for example gemini-pro-orchestrator-neon)", model)
	}
	return nil
}

// antigravityCommandWithoutModel drops any --model flag from a plain agy
// command. A command agent-deck cannot rewrite safely (shell syntax, or an
// executable that is not agy) is refused: set --model in that command instead.
func antigravityCommandWithoutModel(command string) (string, error) {
	trimmed := strings.TrimSpace(command)
	if !antigravityBareLauncher(trimmed) {
		return "", fmt.Errorf("cannot set a model on Antigravity command %q: put --model in the command itself", trimmed)
	}
	tokens, ok := shellwords.Split(trimmed)
	if !ok {
		return "", fmt.Errorf("cannot parse Antigravity command %q", trimmed)
	}
	cmdStart := len(tokens) - len(shellwords.CommandWords(tokens))
	kept := make([]string, 0, len(tokens))
	for idx := 0; idx < len(tokens); idx++ {
		tok := tokens[idx]
		if idx > cmdStart && tok == "--model" {
			idx++ // skip paired value
			continue
		}
		if idx > cmdStart && strings.HasPrefix(tok, "--model=") {
			continue
		}
		quoted, ok := quoteAntigravityToken(tok, true)
		if !ok {
			return "", fmt.Errorf("cannot rewrite Antigravity command %q: put --model in the command itself", trimmed)
		}
		kept = append(kept, quoted)
	}
	return strings.Join(kept, " "), nil
}
