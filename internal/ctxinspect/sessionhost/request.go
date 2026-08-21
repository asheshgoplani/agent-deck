package sessionhost

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// ErrNoInstance is returned by [BuildRequest] when it is handed no session.
var ErrNoInstance = errors.New("sessionhost: no session instance to build a request from")

// RequestOptions carries the parts of a [ctxinspect.Request] that do not come
// from the instance itself.
type RequestOptions struct {
	// SessionRef is how the user named this session on the command line. It is
	// interpolated into command levers so a lever the user copies runs against
	// the right session. Empty falls back to the session title, then to the id.
	SessionRef string
	// Now fixes the report timestamp. Zero means time.Now, and setting it is
	// what makes a golden fixture reproducible.
	Now time.Time
	// Estimator overrides the default token estimator. The zero value uses
	// [ctxinspect.DefaultEstimator].
	Estimator ctxinspect.Estimator
}

// BuildRequest assembles a [ctxinspect.Request] from a live agent-deck
// instance, resolving the per-harness paths an adapter must not resolve for
// itself.
//
// peers are the other instances in the profile. They are mandatory for Claude:
// two live instances sharing one claude_session_id must not both resolve a
// transcript (#1349/#1400), or the panel silently shows another session's
// context. Passing nil disables that check, so callers that hold the instance
// list must pass it.
//
// The returned warnings describe everything that could not be resolved — an
// unresolvable transcript is not an error, it is the difference between an
// observed and a projected report, and the adapter says so itself. An error is
// returned only when there is nothing to build a request from at all.
func BuildRequest(inst *session.Instance, peers []*session.Instance, opts RequestOptions) (ctxinspect.Request, []string, error) {
	if inst == nil {
		return ctxinspect.Request{}, nil, ErrNoInstance
	}

	req := ctxinspect.Request{
		Tool:        strings.TrimSpace(inst.Tool),
		SessionID:   harnessSessionID(inst),
		SessionRef:  sessionRef(inst, opts.SessionRef),
		ProjectPath: inst.ProjectPath,
		Model:       strings.TrimSpace(inst.LaunchModelID()),
		Host:        New(),
		Estimator:   opts.Estimator,
		Now:         opts.Now,
	}

	var warnings []string
	switch {
	case session.IsClaudeCompatible(req.Tool):
		// The dir the process runs under, not the dir that names its account.
		// agent-deck hands many sessions a per-session worker-scratch home; its
		// CLAUDE.md, settings.json and skills are what the session loads, and
		// the profile dir's are not.
		configDir, configSource := session.SpawnClaudeConfigDirForInstance(inst)
		req.ConfigDir = configDir
		if configSource == session.ClaudeConfigSourceAmbient {
			// Not an error and usually not even a surprise — it is the ordinary
			// shape of a session with no configured config_dir. It is still the
			// one case where the directory every figure below was read from was
			// inferred rather than set, and a report that names the provenance
			// of each number owes the same of the directory they came from.
			warnings = append(warnings, fmt.Sprintf(
				"agent-deck exports no CLAUDE_CONFIG_DIR for this session, so it inherits its shell's; the inventories below were read from %s, which is where the resolver lands rather than something observed", configDir))
		}
		path, err := inst.GetJSONLPathChecked(peers)
		if err != nil {
			// A collision is the one case where refusing to resolve is the
			// correct answer: reporting the wrong session's context would be
			// worse than reporting none.
			warnings = append(warnings, "transcript not resolved, so this report is projected rather than observed: "+err.Error())
			break
		}
		if strings.TrimSpace(path) == "" {
			// The guard above resolves against the process-wide config dir.
			// Retry under the dir that actually applies to this instance: an
			// account, conductor or group override, or a per-session scratch
			// home, puts the transcript somewhere the default never looks.
			path = session.ResolveClaudeTranscriptPath(req.ConfigDir, inst.ProjectPath, inst.ClaudeSessionID)
		}
		if strings.TrimSpace(path) == "" && configSource == session.ClaudeConfigSourceWorkerScratch {
			// A scratch home mirrors the profile it was seeded from by symlink,
			// so this second lookup normally succeeds through it. When the
			// mirror is incomplete, the profile dir is still the real place the
			// transcript lives, and finding it there beats reporting none.
			if profileDir := session.GetClaudeConfigDirForInstance(inst); profileDir != configDir {
				path = session.ResolveClaudeTranscriptPath(profileDir, inst.ProjectPath, inst.ClaudeSessionID)
			}
		}
		if strings.TrimSpace(path) == "" {
			warnings = append(warnings, transcriptMissingWarning("Claude transcript", req.SessionID, req.ConfigDir))
			break
		}
		req.TranscriptPath = path

	case session.IsCodexCompatible(req.Tool):
		req.ConfigDir = session.CodexHomeDirForInstance(inst)
		if path := session.CodexRolloutPathForInstance(inst); path != "" {
			req.TranscriptPath = path
		} else {
			warnings = append(warnings, transcriptMissingWarning("Codex rollout", req.SessionID, req.ConfigDir))
		}
	}

	return req, warnings, nil
}

// harnessSessionID returns the harness's own session identifier.
//
// It resolves the compatibility families explicitly rather than deferring to
// [session.Instance.DisplaySessionID], which matches codex by exact tool name:
// a custom [tools.*] entry wrapping codex routes to the Codex adapter here, so
// reading its id through a claude/gemini/opencode/codex-exact switch would hand
// that adapter an empty id and silently downgrade the report to projected.
func harnessSessionID(inst *session.Instance) string {
	switch {
	case session.IsClaudeCompatible(inst.Tool):
		return strings.TrimSpace(inst.ClaudeSessionID)
	case session.IsCodexCompatible(inst.Tool):
		return strings.TrimSpace(inst.CodexSessionID)
	default:
		return strings.TrimSpace(inst.DisplaySessionID())
	}
}

// sessionRef picks the most useful name for command levers: what the user
// typed, then the title they would type, then the id that always resolves.
func sessionRef(inst *session.Instance, explicit string) string {
	for _, candidate := range []string{explicit, inst.Title, inst.ID} {
		if v := strings.TrimSpace(candidate); v != "" {
			return v
		}
	}
	return ""
}

// transcriptMissingWarning explains an absent transcript in terms the user can
// act on, naming both the id that was looked for and where it was looked for.
func transcriptMissingWarning(kind, sessionID, configDir string) string {
	switch {
	case sessionID == "":
		return fmt.Sprintf("no %s: this session has no harness session id yet, so nothing could be observed and the report is projected", strings.ToLower(kind))
	case configDir == "":
		return fmt.Sprintf("no %s found for session id %s; the report is projected", strings.ToLower(kind), sessionID)
	default:
		return fmt.Sprintf("no %s found for session id %s under %s; the report is projected", strings.ToLower(kind), sessionID, configDir)
	}
}
