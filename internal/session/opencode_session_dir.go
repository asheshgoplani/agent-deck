package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// session_move takes a 2.x session out of ProjectPath, so the binding carries the session's directory.

type openCodeServiceSessionGet struct {
	Data openCodeHTTPSessionMetadata `json:"data"`
}

// queryBoundOpenCodeServiceSession reads the bound session by id from the 2.x
// shared service. It returns currentID and records the session's directory
// while the session exists, and "" (keep the binding as is) on any failure.
func (i *Instance) queryBoundOpenCodeServiceSession(projectPath, currentID string) string {
	binary, ok := i.openCodeLaunchBinary()
	if !ok {
		logBoundOpenCodeLookupFailed(currentID, "no opencode binary")
		return ""
	}
	output, err := runOpenCodeServiceCall(binary, projectPath, "session.get", currentID, "")
	if err != nil {
		logBoundOpenCodeLookupFailed(currentID, err.Error())
		return ""
	}
	var got openCodeServiceSessionGet
	if err := json.Unmarshal(output, &got); err != nil {
		logBoundOpenCodeLookupFailed(currentID, err.Error())
		return ""
	}
	if got.Data.ID != currentID {
		logBoundOpenCodeLookupFailed(currentID, fmt.Sprintf("reply carries session %q", got.Data.ID))
		return ""
	}
	directory := got.Data.flatten().Directory

	i.mu.Lock()
	if i.OpenCodeSessionID == currentID {
		i.OpenCodeSessionDir = directory
		i.openCodeSessionDirID = currentID
	}
	i.mu.Unlock()
	sessionLog.Debug("opencode_bound_session_found",
		slog.String("session_id", currentID),
		slog.String("dir", logging.SanitizeValue(directory)))
	return currentID
}

// openCodeSessionDir returns the recorded directory while it still belongs to
// the bound session id, and "" after any rebind or clear.
func (i *Instance) openCodeSessionDir() string {
	if i.OpenCodeSessionID == "" || i.openCodeSessionDirID != i.OpenCodeSessionID {
		return ""
	}
	return i.OpenCodeSessionDir
}

// openCodeResumeDirArg returns the positional directory argument that starts
// a resumed TUI where its session lives, or "" when that is ProjectPath or
// the directory is gone (a removed worktree falls back to ProjectPath).
func (i *Instance) openCodeResumeDirArg() string {
	dir := i.openCodeSessionDir()
	if dir == "" || normalizePath(dir) == normalizePath(i.ProjectPath) {
		return ""
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return " " + shellescape.Quote(dir)
}

const toolDataOpenCodeSessionDirKey = "opencode_session_location"

// The id travels with the directory: tool_data extras outlive a rebind, so a
// directory is only trusted for the id it was recorded with.
type openCodeSessionLocation struct {
	SessionID string `json:"session_id"`
	Directory string `json:"directory"`
}

// WriteOpenCodeSessionDirToToolData merges the bound session's directory into
// tool_data, or removes the key when either value is empty.
func WriteOpenCodeSessionDirToToolData(td json.RawMessage, sessionID, dir string) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if sessionID != "" && dir != "" {
		raw, _ := json.Marshal(openCodeSessionLocation{SessionID: sessionID, Directory: dir})
		m[toolDataOpenCodeSessionDirKey] = raw
	} else {
		delete(m, toolDataOpenCodeSessionDirKey)
	}
	out, _ := json.Marshal(m)
	return out
}

// ReadOpenCodeSessionDirFromToolData returns the recorded directory when it
// was recorded for sessionID, and "" for any other id or a legacy row.
func ReadOpenCodeSessionDirFromToolData(td json.RawMessage, sessionID string) string {
	if len(td) == 0 || sessionID == "" {
		return ""
	}
	var blob struct {
		Location openCodeSessionLocation `json:"opencode_session_location"`
	}
	if err := json.Unmarshal(td, &blob); err != nil || blob.Location.SessionID != sessionID {
		return ""
	}
	return blob.Location.Directory
}

func logBoundOpenCodeLookupFailed(sessionID, cause string) {
	sessionLog.Debug("opencode_bound_session_lookup_failed",
		slog.String("session_id", sessionID),
		slog.String("error", logging.SanitizeValue(cause)))
}
