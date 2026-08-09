package session

import (
	"strings"
)

const maxClaudePeerNameBytes = 64

// ClaudePeerName returns the stable name used to address this managed session
// through Claude Code's ListAgents and SendMessage tools.
func (i *Instance) ClaudePeerName() string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(i.Title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	prefix := strings.Trim(b.String(), "-")
	if prefix == "" {
		prefix = "session"
	}
	suffix := peerIDSuffix(i.ID)
	maxPrefix := maxClaudePeerNameBytes - len(suffix) - 1
	if len(prefix) > maxPrefix {
		prefix = strings.Trim(prefix[:maxPrefix], "-")
	}
	return prefix + "-" + suffix
}

func peerIDSuffix(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() == 8 {
				return b.String()
			}
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// PeerMessagingCandidate reports static compatibility only. Runtime
// reachability still belongs to Claude Code's ListAgents result.
func (i *Instance) PeerMessagingCandidate() bool {
	return i != nil && IsClaudeCompatible(i.Tool)
}

func extraArgsSupplyName(extraArgs []string) bool {
	for _, tok := range extraArgs {
		if tok == "--name" || strings.HasPrefix(tok, "--name=") {
			return true
		}
	}
	return false
}

func (i *Instance) suppliesClaudeName() bool {
	if extraArgsSupplyName(i.ExtraArgs) || commandTokensSupplyName(i.Wrapper) {
		return true
	}
	if def := GetToolDef(i.Tool); def != nil {
		return commandTokensSupplyName(def.Wrapper)
	}
	return false
}

func commandTokensSupplyName(command string) bool {
	for _, tok := range strings.Fields(command) {
		if tok == "--name" || strings.HasPrefix(tok, "--name=") {
			return true
		}
	}
	return false
}

// extraArgsForFork preserves behavioral flags while dropping the parent's
// address. A fork is a distinct reachable peer and must receive its own name.
func extraArgsForFork(extraArgs []string) []string {
	out := make([]string, 0, len(extraArgs))
	for idx := 0; idx < len(extraArgs); idx++ {
		tok := extraArgs[idx]
		if tok == "--name" {
			if idx+1 < len(extraArgs) {
				idx++
			}
			continue
		}
		if strings.HasPrefix(tok, "--name=") {
			continue
		}
		out = append(out, tok)
	}
	return out
}
