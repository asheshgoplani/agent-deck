package session

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Turn origin: who started the turn a session is in right now. The hook
// handler records it on UserPromptSubmit and clears it on Stop, so it is
// set only while an unattended turn runs. Two rules read it:
//
//   - a turn started by a standing order may not add or change orders
//     ("jobs can't make jobs": `agent-deck orders add` refuses), and
//   - a permission prompt in an order or heartbeat turn is denied at once,
//     with a NEED for the owner, instead of waiting for nobody.
//
// A turn the owner started (typed, a chat bridge, anything else) records
// nothing, so a missing file always means "attended".

// Turn origins.
const (
	TurnOriginHeartbeat   = "heartbeat"
	TurnOriginOrderPrefix = "order:"
)

const turnOriginSuffix = ".turn-origin"

// orderWakeRe matches the first line of a standing-order wake
// ("[ORDER <name>] ..."; internal/orders.WakePrefix).
var orderWakeRe = regexp.MustCompile(`^\[ORDER ([a-z0-9][a-z0-9_-]{0,62})\]`)

// TurnOriginForPrompt classifies a prompt: "order:<name>", "heartbeat" or ""
// for an attended turn. A leading send envelope line is skipped.
func TurnOriginForPrompt(prompt string) string {
	p := strings.TrimSpace(prompt)
	if HasSendEnvelope(p) {
		if _, rest, ok := strings.Cut(p, "\n"); ok {
			p = strings.TrimSpace(rest)
		}
	}
	if m := orderWakeRe.FindStringSubmatch(p); m != nil {
		return TurnOriginOrderPrefix + m[1]
	}
	if IsConductorHeartbeatMessage(p) {
		return TurnOriginHeartbeat
	}
	return ""
}

func turnOriginPath(instanceID string) string {
	return filepath.Join(GetHooksDir(), instanceID+turnOriginSuffix)
}

func validTurnOriginID(instanceID string) bool {
	return instanceID != "" && filepath.Base(instanceID) == instanceID && !strings.Contains(instanceID, "..")
}

// RecordTurnOrigin stores the origin of the turn this prompt starts, or
// clears it when the prompt is attended.
func RecordTurnOrigin(instanceID, prompt string) error {
	if !validTurnOriginID(instanceID) {
		return nil
	}
	origin := TurnOriginForPrompt(prompt)
	if origin == "" {
		ClearTurnOrigin(instanceID)
		return nil
	}
	if err := os.MkdirAll(GetHooksDir(), 0o700); err != nil {
		return err
	}
	path := turnOriginPath(instanceID)
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(origin)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

// ClearTurnOrigin marks the session's turn as over (Stop).
func ClearTurnOrigin(instanceID string) {
	if !validTurnOriginID(instanceID) {
		return
	}
	_ = os.Remove(turnOriginPath(instanceID))
}

// LoadTurnOrigin returns the origin of the session's current turn ("" when
// attended or unknown).
func LoadTurnOrigin(instanceID string) string {
	if !validTurnOriginID(instanceID) {
		return ""
	}
	data, err := os.ReadFile(turnOriginPath(instanceID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// IsOrderTurnOrigin reports whether origin is a standing-order turn.
func IsOrderTurnOrigin(origin string) bool {
	return strings.HasPrefix(origin, TurnOriginOrderPrefix)
}
