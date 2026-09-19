package statedb

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Recall phase 1 (docs/recall.md): durable human intent about a session.
//
// Hints, tags and links live in state.db, not in the (future, disposable)
// recall.db index, because they are the one thing a rebuild cannot
// reproduce from transcripts. The tables are created with CREATE TABLE IF
// NOT EXISTS and SchemaVersion deliberately stays at 13: Migrate has no
// downgrade guard, so a bumped version would be rewritten back by every
// older binary that shares the database (other profiles, remote hosts).

// Hint scope kinds. An instance-scoped row is keyed by the agent-deck
// instance id and written at creation or by `session annotate`; a
// harness-session row is keyed by the harness's own conversation id and is
// only ever populated by the binding arbitration path.
const (
	HintScopeInstance       = "instance"
	HintScopeHarnessSession = "harness_session"
)

// Hint sources record who wrote a row, so a derived value never outranks a
// typed one when the recall index ranks on them.
const (
	HintSourceCLICreate = "cli_create"
	HintSourceAnnotate  = "annotate"
	HintSourceHook      = "hook"
	HintSourceConductor = "conductor"
	HintSourceFleet     = "fleet"
	HintSourceDerived   = "derived"
)

// SessionHint is one single-valued key for a scope. Setting the same key
// again replaces the value (UPSERT) and bumps Seq; it never duplicates.
type SessionHint struct {
	ScopeKind string `json:"scope_kind"`
	ScopeID   string `json:"scope_id"`
	Harness   string `json:"harness,omitempty"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	Seq       int    `json:"seq"`
	Source    string `json:"source"`
	Author    string `json:"author,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// SessionTag is one multi-valued label for a scope, ordered by Seq.
type SessionTag struct {
	ScopeKind string `json:"scope_kind"`
	ScopeID   string `json:"scope_id"`
	Tag       string `json:"tag"`
	Seq       int    `json:"seq"`
	Source    string `json:"source"`
	CreatedAt int64  `json:"created_at"`
}

// SessionLink maps an agent-deck instance to a harness conversation id.
// Exactly one row per (session, harness) is authoritative at a time; earlier
// ids stay as history with Authoritative=false.
type SessionLink struct {
	SessionID     string `json:"session_id"`
	Harness       string `json:"harness"`
	NativeID      string `json:"native_id"`
	Host          string `json:"host,omitempty"`
	Authoritative bool   `json:"authoritative"`
	FirstSeen     int64  `json:"first_seen"`
	LastSeen      int64  `json:"last_seen"`
}

// recallSchemaStatements is the phase 1 state.db addition. Additive only;
// see the package comment above for why SchemaVersion does not move.
var recallSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS session_hints (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		scope_kind    TEXT NOT NULL,
		scope_id      TEXT NOT NULL,
		harness       TEXT NOT NULL DEFAULT '',
		key           TEXT NOT NULL,
		value         TEXT NOT NULL,
		seq           INTEGER NOT NULL DEFAULT 0,
		source        TEXT NOT NULL,
		author        TEXT NOT NULL DEFAULT '',
		created_at    INTEGER NOT NULL,
		superseded_at INTEGER NOT NULL DEFAULT 0,
		UNIQUE(scope_kind, scope_id, key)
	)`,
	`CREATE TABLE IF NOT EXISTS session_tags (
		scope_kind TEXT NOT NULL,
		scope_id   TEXT NOT NULL,
		tag        TEXT NOT NULL,
		seq        INTEGER NOT NULL DEFAULT 0,
		source     TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		deleted_at INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(scope_kind, scope_id, tag)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_hints_scope ON session_hints(scope_kind, scope_id)`,
	`CREATE INDEX IF NOT EXISTS idx_hints_kv ON session_hints(key, value)`,
	`CREATE INDEX IF NOT EXISTS idx_tags_tag ON session_tags(tag)`,
	`CREATE TABLE IF NOT EXISTS session_links (
		session_id    TEXT NOT NULL,
		harness       TEXT NOT NULL,
		native_id     TEXT NOT NULL,
		host          TEXT NOT NULL DEFAULT '',
		authoritative INTEGER NOT NULL DEFAULT 0,
		first_seen    INTEGER NOT NULL,
		last_seen     INTEGER NOT NULL,
		PRIMARY KEY (session_id, harness, native_id)
	)`,
}

// migrateRecallTables creates the recall tables inside the Migrate transaction.
func migrateRecallTables(e execer) error {
	for _, stmt := range recallSchemaStatements {
		if _, err := e.Exec(stmt); err != nil {
			return fmt.Errorf("statedb: create recall tables: %w", err)
		}
	}
	return nil
}

// execer is the subset of *sql.DB / *sql.Tx the helpers below need.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// execAffected runs one write under the busy retry and reports whether it
// touched any row, which is what the unset/remove/retract paths return.
func (s *StateDB) execAffected(query string, args ...any) (bool, error) {
	var n int64
	err := withBusyRetry(func() error {
		res, err := s.db.Exec(query, args...)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n > 0, err
}

// pruneOrphanedRecallRows removes the instance-scoped hints, tags and links
// whose instance row is gone. Called by every path that deletes instances.
// The predicate is "no matching instances row" rather than a caller-supplied
// id list, so it stays correct for the sweep's DELETE ... NOT IN as well as
// for single deletes.
func pruneOrphanedRecallRows(e execer) error {
	stmts := []string{
		`DELETE FROM session_hints WHERE scope_kind = '` + HintScopeInstance + `'
		   AND scope_id NOT IN (SELECT id FROM instances)`,
		`DELETE FROM session_tags WHERE scope_kind = '` + HintScopeInstance + `'
		   AND scope_id NOT IN (SELECT id FROM instances)`,
		`DELETE FROM session_links WHERE session_id NOT IN (SELECT id FROM instances)`,
	}
	for _, stmt := range stmts {
		if _, err := e.Exec(stmt); err != nil {
			return fmt.Errorf("statedb: prune recall rows: %w", err)
		}
	}
	return nil
}

func validHintScope(scopeKind, scopeID string) error {
	if scopeKind != HintScopeInstance && scopeKind != HintScopeHarnessSession {
		return fmt.Errorf("statedb: invalid hint scope kind %q", scopeKind)
	}
	if strings.TrimSpace(scopeID) == "" {
		return fmt.Errorf("statedb: empty hint scope id")
	}
	return nil
}

// validHintToken accepts the identifier shape hint keys and tags share: no
// whitespace, no '=' (the CLI splits key=value on it), non-empty.
func validHintToken(what, s string) error {
	if s == "" {
		return fmt.Errorf("statedb: empty %s", what)
	}
	if strings.ContainsAny(s, " \t\r\n=") {
		return fmt.Errorf("statedb: %s %q must not contain whitespace or '='", what, s)
	}
	return nil
}

// SetSessionHint writes one single-valued hint, replacing any existing value
// for the same (scope, key). Correcting ticket=SB-412 to SB-413 therefore
// yields one live row, not two.
func (s *StateDB) SetSessionHint(scopeKind, scopeID, key, value, source, author string) error {
	if err := validHintScope(scopeKind, scopeID); err != nil {
		return err
	}
	if err := validHintToken("hint key", key); err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("statedb: empty value for hint %q", key)
	}
	if source == "" {
		source = HintSourceAnnotate
	}
	return withBusyRetry(func() error {
		_, err := s.db.Exec(`
			INSERT INTO session_hints (scope_kind, scope_id, key, value, seq, source, author, created_at)
			VALUES (?, ?, ?, ?, 0, ?, ?, ?)
			ON CONFLICT(scope_kind, scope_id, key) DO UPDATE SET
			  value = excluded.value,
			  seq = session_hints.seq + 1,
			  source = excluded.source,
			  author = excluded.author,
			  created_at = excluded.created_at`,
			scopeKind, scopeID, key, value, source, author, time.Now().Unix())
		return err
	})
}

// UnsetSessionHint removes one hint key. Returns whether a row was removed.
func (s *StateDB) UnsetSessionHint(scopeKind, scopeID, key string) (bool, error) {
	if err := validHintScope(scopeKind, scopeID); err != nil {
		return false, err
	}
	return s.execAffected(`DELETE FROM session_hints WHERE scope_kind = ? AND scope_id = ? AND key = ?`,
		scopeKind, scopeID, key)
}

// ListSessionHints returns a scope's hints ordered by key.
func (s *StateDB) ListSessionHints(scopeKind, scopeID string) ([]SessionHint, error) {
	rows, err := s.db.Query(`
		SELECT scope_kind, scope_id, harness, key, value, seq, source, author, created_at
		  FROM session_hints WHERE scope_kind = ? AND scope_id = ? ORDER BY key`, scopeKind, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionHint
	for rows.Next() {
		var h SessionHint
		if err := rows.Scan(&h.ScopeKind, &h.ScopeID, &h.Harness, &h.Key, &h.Value, &h.Seq, &h.Source, &h.Author, &h.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// AddSessionTag adds one tag to a scope. Adding a tag that already exists is
// a no-op; adding one that was removed revives it at the end of the order.
func (s *StateDB) AddSessionTag(scopeKind, scopeID, tag, source string) error {
	if err := validHintScope(scopeKind, scopeID); err != nil {
		return err
	}
	if err := validHintToken("tag", tag); err != nil {
		return err
	}
	if source == "" {
		source = HintSourceAnnotate
	}
	return withBusyRetry(func() error {
		_, err := s.db.Exec(`
			INSERT INTO session_tags (scope_kind, scope_id, tag, seq, source, created_at, deleted_at)
			VALUES (?, ?, ?,
			  COALESCE((SELECT MAX(seq) + 1 FROM session_tags WHERE scope_kind = ? AND scope_id = ?), 0),
			  ?, ?, 0)
			ON CONFLICT(scope_kind, scope_id, tag) DO UPDATE SET
			  seq = CASE WHEN session_tags.deleted_at = 0 THEN session_tags.seq ELSE excluded.seq END,
			  source = CASE WHEN session_tags.deleted_at = 0 THEN session_tags.source ELSE excluded.source END,
			  created_at = CASE WHEN session_tags.deleted_at = 0 THEN session_tags.created_at ELSE excluded.created_at END,
			  deleted_at = 0`,
			scopeKind, scopeID, tag, scopeKind, scopeID, source, time.Now().Unix())
		return err
	})
}

// RemoveSessionTag soft-deletes one tag. Returns whether a live tag was removed.
func (s *StateDB) RemoveSessionTag(scopeKind, scopeID, tag string) (bool, error) {
	if err := validHintScope(scopeKind, scopeID); err != nil {
		return false, err
	}
	return s.execAffected(`UPDATE session_tags SET deleted_at = ?
		WHERE scope_kind = ? AND scope_id = ? AND tag = ? AND deleted_at = 0`,
		time.Now().Unix(), scopeKind, scopeID, tag)
}

// ListSessionTags returns a scope's live tags in insertion order.
func (s *StateDB) ListSessionTags(scopeKind, scopeID string) ([]SessionTag, error) {
	rows, err := s.db.Query(`
		SELECT scope_kind, scope_id, tag, seq, source, created_at
		  FROM session_tags WHERE scope_kind = ? AND scope_id = ? AND deleted_at = 0 ORDER BY seq, tag`,
		scopeKind, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionTag
	for rows.Next() {
		var t SessionTag
		if err := rows.Scan(&t.ScopeKind, &t.ScopeID, &t.Tag, &t.Seq, &t.Source, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpsertSessionLink records that instance sessionID is (or was) the harness
// conversation nativeID. With authoritative=true every other native id for
// the same (session, harness) is demoted, so at most one row is current.
func (s *StateDB) UpsertSessionLink(sessionID, harness, nativeID, host string, authoritative bool) error {
	return withBusyRetry(func() error {
		return upsertSessionLink(s.db, sessionID, harness, nativeID, host, authoritative, time.Now())
	})
}

func upsertSessionLink(e execer, sessionID, harness, nativeID, host string, authoritative bool, seen time.Time) error {
	if sessionID == "" || harness == "" || nativeID == "" {
		return fmt.Errorf("statedb: session link needs session, harness and native id")
	}
	if seen.IsZero() {
		seen = time.Now()
	}
	at := seen.Unix()
	auth := 0
	if authoritative {
		auth = 1
		if _, err := e.Exec(`UPDATE session_links SET authoritative = 0
			WHERE session_id = ? AND harness = ? AND native_id <> ?`, sessionID, harness, nativeID); err != nil {
			return err
		}
	}
	_, err := e.Exec(`
		INSERT INTO session_links (session_id, harness, native_id, host, authoritative, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, harness, native_id) DO UPDATE SET
		  last_seen = excluded.last_seen,
		  authoritative = excluded.authoritative,
		  host = CASE WHEN excluded.host = '' THEN session_links.host ELSE excluded.host END`,
		sessionID, harness, nativeID, host, auth, at, at)
	return err
}

// demoteSessionLinks clears authority for every link of an instance (all
// harnesses). Used when a binding is cleared without a replacement.
func demoteSessionLinks(e execer, sessionID string) error {
	_, err := e.Exec(`UPDATE session_links SET authoritative = 0 WHERE session_id = ?`, sessionID)
	return err
}

// RetractSessionLink removes the link for a candidate the adoption
// arbitration rejected. Returns whether a row existed.
func (s *StateDB) RetractSessionLink(sessionID, harness, nativeID string) (bool, error) {
	return s.execAffected(`DELETE FROM session_links WHERE session_id = ? AND harness = ? AND native_id = ?`,
		sessionID, harness, nativeID)
}

// ListSessionLinks returns every recorded harness conversation id for an
// instance, authoritative first, then most recently seen.
func (s *StateDB) ListSessionLinks(sessionID string) ([]SessionLink, error) {
	rows, err := s.db.Query(`
		SELECT session_id, harness, native_id, host, authoritative, first_seen, last_seen
		  FROM session_links WHERE session_id = ?
		 ORDER BY authoritative DESC, last_seen DESC, native_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionLink
	for rows.Next() {
		var l SessionLink
		var auth int
		if err := rows.Scan(&l.SessionID, &l.Harness, &l.NativeID, &l.Host, &auth, &l.FirstSeen, &l.LastSeen); err != nil {
			return nil, err
		}
		l.Authoritative = auth != 0
		out = append(out, l)
	}
	return out, rows.Err()
}
