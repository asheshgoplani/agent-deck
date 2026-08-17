// Scope for a persisted custom-tool conversation id.
//
// generic_session_id survives reboot, which is the point of it — but an id that
// outlives the process also outlives the assumptions under which it was
// captured. Two of those matter:
//
//   - The TOOL. `[tools.*]` entries are operator-defined and a session's tool
//     can be changed (`session set <id> tool <other>`), or the same tool name
//     can be repointed at a different command in config.toml. Replaying a
//     conversation id captured from one CLI into a different one resumes
//     someone else's conversation, or fails in a way that looks like data loss.
//
//   - The COMMAND. `command` is independently mutable and restart-required, so
//     one custom CLI can replace another while the tool NAME stays the same.
//     The tool name alone would still match, and the old conversation id would
//     be handed to the new executable.
//
//   - The EXECUTION LOCATION. An --ssh session runs its tool on a remote host
//     whose conversation store is not the controller's. Comparing project paths
//     does not tell local and remote apart — that mistake is the root cause
//     behind #1850-#1853, and internal/session/location.go exists to encode the
//     rule. A resume id captured on host A must not be handed to the same tool
//     running locally, or on host B.
//
// So the binding records where it came from and is only honored when the
// current session still matches. A mismatch is not an error: the id simply is
// not eligible for resume, and the tool starts a fresh conversation — the same
// outcome as never having had an id, which is the safe direction. The stored id
// is deliberately left in place so that moving a session back to its original
// tool or host makes it resumable again.
//
// Every production path that records an id records its scope in the same write,
// so an id with NO recorded scope did not come from one of them. That case is
// allowed through rather than refused — refusing it would turn "agent-deck
// wrote this differently than expected" into "your conversation is gone", which
// is a worse failure than the one being guarded against, and the check would
// then be enforcing an invariant about our own writers rather than about the
// binding. It is logged, because a scope-less binding means some writer skipped
// the scope and that is worth knowing. A recorded scope that DISAGREES is
// refused: that is a real conflict, not a missing record.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

const (
	toolDataGenericSessionToolKey     = "generic_session_tool"
	toolDataGenericSessionLocationKey = "generic_session_location"
	toolDataGenericSessionCommandKey  = "generic_session_command"
)

// genericSessionScope is the (tool, location) pair a conversation id was bound
// under.
type genericSessionScope struct {
	Tool     string
	Command  string
	Location string
}

// complete reports whether a scope says enough to be checked against. Command
// is not required: a session can legitimately carry an empty command (the tool
// definition supplies it), and demanding one would refuse bindings that are
// perfectly well scoped by tool and location.
func (s genericSessionScope) complete() bool {
	return s.Tool != "" && s.Location != ""
}

// currentGenericSessionScope is the scope this instance would bind under now.
func (i *Instance) currentGenericSessionScope() genericSessionScope {
	return genericSessionScope{
		Tool:     strings.TrimSpace(i.Tool),
		Command:  strings.TrimSpace(i.Command),
		Location: LocationOf(i).String(),
	}
}

// recordedGenericSessionScope is the scope stored alongside the persisted id.
func (i *Instance) recordedGenericSessionScope() genericSessionScope {
	return genericSessionScope{
		Tool:     strings.TrimSpace(i.GenericSessionTool),
		Command:  strings.TrimSpace(i.GenericSessionCommand),
		Location: strings.TrimSpace(i.GenericSessionLocation),
	}
}

// genericSessionScopeMismatch returns "" when the persisted id may be resumed,
// or a short reason why it may not.
func (i *Instance) genericSessionScopeMismatch() string {
	recorded := i.recordedGenericSessionScope()
	if !recorded.complete() {
		sessionLog.Debug("generic_session_scope_missing",
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("tool", logging.SanitizeValue(i.Tool)),
			slog.String("session_id_fingerprint", fingerprintSessionID(i.GenericSessionID)))
		return ""
	}
	current := i.currentGenericSessionScope()
	if recorded.Tool != current.Tool {
		return "conversation id was captured for tool " + recorded.Tool + ", session now runs " + current.Tool
	}
	if recorded.Command != current.Command {
		return "conversation id was captured for command " + recorded.Command + ", session now runs " + current.Command
	}
	if recorded.Location != current.Location {
		return "conversation id was captured at " + recorded.Location + ", session now runs at " + current.Location
	}
	return ""
}

// logGenericResumeRefusal records a refusal without putting the conversation id
// in the log. See fingerprintSessionID.
func (i *Instance) logGenericResumeRefusal(reason string) {
	sessionLog.Warn("generic_resume_refused",
		slog.String("instance_id", logging.SanitizeValue(i.ID)),
		slog.String("tool", logging.SanitizeValue(i.Tool)),
		slog.String("session_id_fingerprint", fingerprintSessionID(i.GenericSessionID)),
		slog.String("reason", logging.SanitizeValue(reason)),
		slog.String("action", "start_fresh"),
	)
}

// fingerprintSessionID renders a conversation id for logs without disclosing it.
//
// A custom tool's conversation id is a handle into a third-party product's
// transcript store, and agent-deck writes its logs to disk and ships them in
// bug reports. The built-in ids predate this and are still logged whole; new
// surface does not have to repeat that. A truncated SHA-256 keeps every
// property an operator actually needs from a log line — telling two sessions
// apart, and seeing that an id changed — without carrying the id itself.
//
// Empty stays empty rather than hashing to a constant, so "no id" reads as no
// id instead of as some particular one.
func fingerprintSessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	return "sha256:" + hex.EncodeToString(sum[:])[:12]
}

// WriteGenericSessionScopeToToolData merges the scope keys into a tool_data
// blob, following exactly the omission/explicit-empty protocol the id itself
// uses (see WriteGenericSessionIDToToolData): omission means "this writer has
// not observed the binding, preserve what is there", and an explicit empty
// string is an intentional clear.
//
// It is a separate function from the id writer, and a separate pair of keys,
// so that a writer that knows the id also always states the scope — a single
// call that wrote the id but left the scope stale is exactly the failure this
// guards against.
func WriteGenericSessionScopeToToolData(td json.RawMessage, tool, command, location string, intentionalClear bool) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if tool == "" && location == "" {
		if intentionalClear {
			m[toolDataGenericSessionToolKey] = json.RawMessage(`""`)
			m[toolDataGenericSessionCommandKey] = json.RawMessage(`""`)
			m[toolDataGenericSessionLocationKey] = json.RawMessage(`""`)
		} else {
			delete(m, toolDataGenericSessionToolKey)
			delete(m, toolDataGenericSessionCommandKey)
			delete(m, toolDataGenericSessionLocationKey)
		}
		out, _ := json.Marshal(m)
		return out
	}
	rawTool, _ := json.Marshal(tool)
	rawCmd, _ := json.Marshal(command)
	rawLoc, _ := json.Marshal(location)
	m[toolDataGenericSessionToolKey] = rawTool
	m[toolDataGenericSessionCommandKey] = rawCmd
	m[toolDataGenericSessionLocationKey] = rawLoc
	out, _ := json.Marshal(m)
	return out
}

// ReadGenericSessionScopeFromToolData extracts the recorded scope. Missing,
// malformed or legacy blobs read as empty, which is not resumable.
func ReadGenericSessionScopeFromToolData(td json.RawMessage) (tool, command, location string) {
	if len(td) == 0 {
		return "", "", ""
	}
	var blob struct {
		Tool     string `json:"generic_session_tool"`
		Command  string `json:"generic_session_command"`
		Location string `json:"generic_session_location"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.Tool, blob.Command, blob.Location
}

// genericScopeTool, genericScopeCommand and genericScopeLocation are
// single-value accessors for the two load paths, which build their structs
// field-by-field.
func genericScopeTool(td json.RawMessage) string {
	tool, _, _ := ReadGenericSessionScopeFromToolData(td)
	return tool
}

func genericScopeCommand(td json.RawMessage) string {
	_, command, _ := ReadGenericSessionScopeFromToolData(td)
	return command
}

func genericScopeLocation(td json.RawMessage) string {
	_, _, location := ReadGenericSessionScopeFromToolData(td)
	return location
}
