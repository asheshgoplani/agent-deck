package statedb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ClaudeStatusline contains only the status bar's public metadata. Unknown
// payload fields (including prompts and transcript paths) cannot be persisted.
type ClaudeStatusline struct {
	ClaudeSessionID string                   `json:"claude_session_id"`
	CapturedAt      string                   `json:"captured_at"`
	Model           *ClaudeStatuslineModel   `json:"model"`
	Cwd             string                   `json:"cwd"`
	ContextWindow   *ClaudeStatuslineContext `json:"context_window"`
	RateLimits      *ClaudeStatuslineLimits  `json:"rate_limits"`
	// Account is the configured Claude account slot whose config dir the
	// status line ran under; absent when that is not a configured slot.
	Account string `json:"account,omitempty"`
	// PermissionMode is Claude's current permission mode (from the payload,
	// else the transcript's latest permissionMode); absent when unknown.
	PermissionMode string `json:"permission_mode,omitempty"`
}

type ClaudeStatuslineModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type ClaudeStatuslineContext struct {
	UsedPercentage    *float64 `json:"used_percentage"`
	ContextWindowSize *int64   `json:"context_window_size"`
	TotalInputTokens  *int64   `json:"total_input_tokens"`
	TotalOutputTokens *int64   `json:"total_output_tokens"`
}

type ClaudeStatuslineLimits struct {
	FiveHour *ClaudeStatuslineWindow `json:"five_hour"`
	SevenDay *ClaudeStatuslineWindow `json:"seven_day"`
}

type ClaudeStatuslineWindow struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *int64   `json:"resets_at"`
}

const claudeStatuslineSchema = `CREATE TABLE IF NOT EXISTS claude_statuslines (
 claude_session_id TEXT PRIMARY KEY,
 session_id TEXT,
 captured_at INTEGER NOT NULL,
 record TEXT NOT NULL
)`

// SaveClaudeStatusline atomically resolves ownership and replaces the last
// record. Registry deletion prunes linked records with the other session metadata.
// Unlinked records are capped so arbitrary native IDs cannot grow the cache.
func (s *StateDB) SaveClaudeStatusline(record ClaudeStatusline) (string, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	var sessionID string
	err = withBusyRetry(func() error {
		sessionID = ""
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		err = tx.QueryRow(`SELECT min(id) FROM instances WHERE tool = 'claude'
   AND json_extract(tool_data, '$.claude_session_id') = ? HAVING count(*) = 1`, record.ClaudeSessionID).Scan(&sessionID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var owner any
		if sessionID != "" {
			owner = sessionID
		}
		_, err = tx.Exec(`INSERT INTO claude_statuslines (claude_session_id, session_id, captured_at, record)
   VALUES (?, ?, ?, ?) ON CONFLICT(claude_session_id) DO UPDATE SET
   session_id = excluded.session_id,
   captured_at = excluded.captured_at, record = excluded.record`, record.ClaudeSessionID, owner, time.Now().UnixMilli(), string(raw))
		if err != nil {
			return err
		}
		// Keep only the current conversation for a known owner, and at most 128
		// unlinked records. The events log has its own bounded retention policy.
		if sessionID != "" {
			if _, err = tx.Exec(`DELETE FROM claude_statuslines WHERE session_id = ? AND claude_session_id != ?`, sessionID, record.ClaudeSessionID); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(`DELETE FROM claude_statuslines WHERE session_id IS NOT NULL AND session_id NOT IN (SELECT id FROM instances)`); err != nil {
			return err
		}
		if _, err = tx.Exec(`DELETE FROM claude_statuslines WHERE session_id IS NULL AND claude_session_id NOT IN
   (SELECT claude_session_id FROM claude_statuslines WHERE session_id IS NULL ORDER BY captured_at DESC, rowid DESC LIMIT 128)`); err != nil {
			return err
		}
		return tx.Commit()
	})
	return sessionID, err
}

// ClaudeStatuslineForSession resolves an exact ID or unambiguous title without
// tmux probes. Joining on the current native ID also finds a pre-binding record.
func (s *StateDB) ClaudeStatuslineForSession(target string) (*ClaudeStatusline, error) {
	var nativeID string
	err := s.db.QueryRow(`SELECT json_extract(tool_data, '$.claude_session_id') FROM instances
  WHERE tool = 'claude' AND (id = ? OR (title = ? AND NOT EXISTS (SELECT 1 FROM instances WHERE id = ?)
  AND (SELECT count(*) FROM instances WHERE title = ?) = 1))`, target, target, target, target).Scan(&nativeID)
	if err != nil {
		return nil, err
	}
	var owners int
	if err = s.db.QueryRow(`SELECT count(*) FROM instances WHERE tool = 'claude' AND json_extract(tool_data, '$.claude_session_id') = ?`, nativeID).Scan(&owners); err != nil {
		return nil, err
	}
	if owners != 1 {
		return nil, sql.ErrNoRows
	}
	var raw []byte
	if err = s.db.QueryRow(`SELECT record FROM claude_statuslines WHERE claude_session_id = ?`, nativeID).Scan(&raw); err != nil {
		return nil, err
	}
	var record ClaudeStatusline
	if err = json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	return &record, nil
}
