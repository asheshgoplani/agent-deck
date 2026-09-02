package session

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Muse session discovery.
//
// Muse persists one directory per session, sharded by date:
//
//	<root>/YYYY/MM/DD/<uuid>/session.jsonl
//
// where <root> is $XDG_DATA_HOME/muse/sessions when XDG_DATA_HOME is set
// (muse honors it; observed a pane inheriting XDG_DATA_HOME persist plugin
// cache and tracing underneath it) and ~/.local/share/muse/sessions
// otherwise. Each session.jsonl line is either a framed transaction
// ({"retained_frame":..., "children":[{"record_json":"<escaped record>"}]})
// or a bare record; the workspace binding lives in the record with
// payload_type "runtime.session.metadata" as payload.record.workspace_root
// (symlink-resolved, e.g. /private/tmp/...).
//
// Restart-resume reads the store on EVERY restart and takes the newest
// session for the working directory (bounded by the instance's last start),
// so a pruned session simply yields the current newest, or "" for a fresh
// boot — the same self-healing shape as the deepseek restart path.

// museSessionsRootOverride redirects the store root in tests (mirrors
// geminiConfigDirOverride). Production code uses the real resolution.
var museSessionsRootOverride string

// museDiscoveryMaxFiles bounds a single store scan. Session logs can reach
// megabytes, but the metadata record sits at the top of the file (observed
// at line 2 of a 7.6MB log), so each file costs one short prefix read;
// the cap only guards pathological stores.
const museDiscoveryMaxFiles = 2000

// museDiscoverySkew tolerates clock skew between the instance's recorded
// start time and session-file mtimes when bounding a resume scan.
const museDiscoverySkew = 5 * time.Minute

// MuseSessionInfo is one discovered muse session.
type MuseSessionInfo struct {
	SessionID     string
	WorkspaceRoot string
	ProviderID    string
	LastUpdated   time.Time // file mtime (cheap recency signal)
}

// GetMuseSessionsRoot returns the muse session store root.
func GetMuseSessionsRoot() string {
	if museSessionsRootOverride != "" {
		return museSessionsRootOverride
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		if abs, err := filepath.Abs(xdg); err == nil {
			return filepath.Join(abs, "muse", "sessions")
		}
		return filepath.Join(xdg, "muse", "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "muse", "sessions")
}

// museRecord is one inner session-log record (framed or bare).
type museRecord struct {
	Stream struct {
		ID string `json:"id"`
	} `json:"stream"`
	RecordedAt  int64  `json:"recorded_at"`
	PayloadType string `json:"payload_type"`
	Payload     struct {
		Record struct {
			WorkspaceRoot string `json:"workspace_root"`
			ProviderID    string `json:"provider_id"`
		} `json:"record"`
	} `json:"payload"`
}

// museFrame is one framed session.jsonl line.
type museFrame struct {
	Children []struct {
		RecordJSON string `json:"record_json"`
	} `json:"children"`
}

// parseMuseMetadataLine extracts the session-metadata record from one
// session.jsonl line. It returns nil when the line carries no metadata
// record (the common case: permission frames, tool calls, echoes).
func parseMuseMetadataLine(line string) *museRecord {
	if !strings.Contains(line, "runtime.session.metadata") {
		return nil
	}
	tryRecord := func(raw string) *museRecord {
		var rec museRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return nil
		}
		if rec.PayloadType != "runtime.session.metadata" {
			return nil
		}
		return &rec
	}
	// Framed line: unwrap children first.
	var frame museFrame
	if err := json.Unmarshal([]byte(line), &frame); err == nil {
		for _, child := range frame.Children {
			if rec := tryRecord(child.RecordJSON); rec != nil {
				return rec
			}
		}
	}
	// Bare record line.
	return tryRecord(line)
}

// readMuseSessionMetadata returns the FIRST metadata record in a session
// log (workspace binding is written at session start) without reading the
// whole file: it streams lines and stops at the first hit.
func readMuseSessionMetadata(sessionFile string) *museRecord {
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil
	}
	// Lines are long (framed transactions); scan incrementally and stop at
	// the first metadata line. A 64KB prefix covers the startup records in
	// every observed log; fall back to the whole file past that.
	const prefixLimit = 64 * 1024
	text := string(data)
	if len(text) > prefixLimit {
		if rec := scanMuseMetadataPrefix(text[:prefixLimit]); rec != nil {
			return rec
		}
	}
	return scanMuseMetadataPrefix(text)
}

// scanMuseMetadataPrefix returns the first metadata record in text.
func scanMuseMetadataPrefix(text string) *museRecord {
	start := 0
	for start < len(text) {
		end := strings.IndexByte(text[start:], '\n')
		var line string
		if end < 0 {
			line = text[start:]
			start = len(text)
		} else {
			line = text[start : start+end]
			start += end + 1
		}
		if rec := parseMuseMetadataLine(line); rec != nil {
			return rec
		}
	}
	return nil
}

// normalizeMuseWorkspace absolutizes and symlink-resolves a workspace path
// for comparison: muse records the resolved path (/private/tmp/...), while
// callers may hold the unresolved one (/tmp/...).
func normalizeMuseWorkspace(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return filepath.Clean(abs)
}

// ListMuseSessions scans the session store (newest files first, bounded)
// and returns every session carrying a workspace binding.
func ListMuseSessions() []MuseSessionInfo {
	root := GetMuseSessionsRoot()
	if root == "" {
		return nil
	}
	type candidate struct {
		path  string
		mtime time.Time
	}
	var files []candidate
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "session.jsonl" {
			return nil
		}
		if len(files) >= museDiscoveryMaxFiles {
			return filepath.SkipAll
		}
		if info, err := d.Info(); err == nil {
			files = append(files, candidate{path: path, mtime: info.ModTime()})
		}
		return nil
	})
	sort.Slice(files, func(a, b int) bool {
		return files[a].mtime.After(files[b].mtime)
	})

	var out []MuseSessionInfo
	for _, f := range files {
		rec := readMuseSessionMetadata(f.path)
		if rec == nil || rec.Stream.ID == "" {
			continue
		}
		out = append(out, MuseSessionInfo{
			SessionID:     rec.Stream.ID,
			WorkspaceRoot: rec.Payload.Record.WorkspaceRoot,
			ProviderID:    rec.Payload.Record.ProviderID,
			LastUpdated:   f.mtime,
		})
	}
	return out
}

// FindLatestMuseSession returns the newest session ID bound to workspace.
// since bounds the scan when non-zero (file mtime must be newer than
// since minus skew); zero since scans unbound. Returns "" when nothing
// matches.
func FindLatestMuseSession(workspace string, since time.Time) string {
	want := normalizeMuseWorkspace(workspace)
	if want == "" {
		return ""
	}
	var bound time.Time
	if !since.IsZero() {
		bound = since.Add(-museDiscoverySkew)
	}
	for _, s := range ListMuseSessions() {
		if normalizeMuseWorkspace(s.WorkspaceRoot) != want {
			continue
		}
		if !bound.IsZero() && s.LastUpdated.Before(bound) {
			continue
		}
		return s.SessionID
	}
	return ""
}
