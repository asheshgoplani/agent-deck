package session

import (
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// claudeTitleName preserves the operator's title as one argv value. Controls
// cannot be displayed safely; invalid titles omit the default instead of being
// normalized into a different, potentially colliding name.
func claudeTitleName(title string) string {
	if !utf8.ValidString(title) {
		return ""
	}
	for _, r := range title {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029' {
			return ""
		}
	}
	return title
}

func extraArgsSupplyName(extraArgs []string) bool {
	for _, tok := range extraArgs {
		if tok == "--name" || strings.HasPrefix(tok, "-n") || strings.HasPrefix(tok, "--name=") {
			return true
		}
	}
	return false
}

// Explicit session selectors can override the launcher's owned conversation.
// Preserve these operator arguments, but do not give another target our name.
func extraArgsSelectSession(extraArgs []string) bool {
	for _, tok := range extraArgs {
		// Conservatively include attached short values and option groups.
		if strings.HasPrefix(tok, "-r") || strings.HasPrefix(tok, "-c") {
			return true
		}
		flag, _, _ := strings.Cut(tok, "=")
		switch flag {
		case "--resume", "-r", "--continue", "-c", "--session-id", "--fork-session":
			return true
		}
	}
	return false
}

// An absent setting defaults to true. An unreadable policy is not consent,
// including a permission change while LoadUserConfig still has a cached value.
func pushTitleEnabled() bool {
	path, err := GetUserConfigPath()
	if err != nil {
		return false
	}
	file, err := os.Open(path)
	if err != nil && !os.IsNotExist(err) {
		return false
	}
	if file != nil {
		_ = file.Close()
	}
	cfg, err := LoadUserConfig()
	return err == nil && cfg != nil && cfg.GetPushTitle()
}

// ClaudeLaunchName supplies a default for a supported Claude startup command.
// It never reads an agent registry or sends input to a running process. Existing
// startup builders retain ownership of account and conversation selection.
func (i *Instance) ClaudeLaunchName() string {
	if i == nil || !IsClaudeCompatible(i.Tool) || extraArgsSupplyName(i.ExtraArgs) || extraArgsSelectSession(i.ExtraArgs) || !pushTitleEnabled() {
		return ""
	}
	// Match the resume builder's generated-fork distinction. Arbitrary per-row
	// commands bypass the normal launch builder and have no naming contract.
	if i.Command != "" && i.Command != "claude" && !commandHasToken(i.Command, "--fork-session") && !commandHasToken(i.Command, "--session-id") {
		return ""
	}
	return claudeTitleName(i.GetTitleThreadSafe())
}
