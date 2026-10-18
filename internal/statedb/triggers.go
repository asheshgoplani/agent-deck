package statedb

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// Triggers (issue #2702): everything that wakes a session. A trigger row is
// one sender (a launchd job, a systemd unit, a watcher, a heartbeat, a
// channel, ...) found either by a tagged send (push) or by the unit scan
// (scan). trigger_fires is its bounded fire log. Both tables are additive
// (CREATE TABLE IF NOT EXISTS), so SchemaVersion does not move.
var triggerSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS triggers (
		id           TEXT PRIMARY KEY,
		kind         TEXT NOT NULL,
		source       TEXT NOT NULL,
		host         TEXT NOT NULL DEFAULT '',
		config_path  TEXT NOT NULL DEFAULT '',
		verb         TEXT NOT NULL DEFAULT '',
		target       TEXT NOT NULL DEFAULT '',
		target_by    TEXT NOT NULL DEFAULT '',
		target_profile TEXT NOT NULL DEFAULT '',
		schedule     TEXT NOT NULL DEFAULT '',
		unit         TEXT NOT NULL DEFAULT '',
		unit_kind    TEXT NOT NULL DEFAULT '',
		pushed       INTEGER NOT NULL DEFAULT 0,
		scanned      INTEGER NOT NULL DEFAULT 0,
		inferred     INTEGER NOT NULL DEFAULT 0,
		muted_until  INTEGER NOT NULL DEFAULT 0,
		created_at   INTEGER NOT NULL,
		updated_at   INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS trigger_fires (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		trigger_id        TEXT NOT NULL,
		ts                INTEGER NOT NULL,
		target            TEXT NOT NULL DEFAULT '',
		target_session_id TEXT NOT NULL DEFAULT '',
		outcome           TEXT NOT NULL DEFAULT '',
		line              TEXT NOT NULL DEFAULT '',
		text_hash         TEXT NOT NULL DEFAULT '',
		noop              INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_trigger_fires_trigger_ts ON trigger_fires(trigger_id, ts DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_trigger_fires_session_ts ON trigger_fires(target_session_id, ts DESC)`,
}

func migrateTriggerTables(e execer) error {
	for _, stmt := range triggerSchemaStatements {
		if _, err := e.Exec(stmt); err != nil {
			return fmt.Errorf("statedb: create trigger tables: %w", err)
		}
	}
	return nil
}

// Fire log retention: fires older than TriggerFireRetention are dropped and at
// most TriggerFireMaxPerTrigger rows are kept per trigger. Both run on every
// insert, so the log stays bounded without a sweeper.
const (
	TriggerFireRetention     = 7 * 24 * time.Hour
	TriggerFireMaxPerTrigger = 2000
)

// triggersRevKey is the metadata key bumped on every trigger write. Readers
// (the TUI panel) poll it to refresh live without rereading the tables.
const triggersRevKey = "triggers_rev"

// TriggerRow is one trigger as stored.
type TriggerRow struct {
	ID            string
	Kind          string
	Source        string
	Host          string
	ConfigPath    string
	Verb          string
	Target        string
	TargetBy      string
	TargetProfile string
	Schedule      string
	Unit          string
	UnitKind      string // launchd | systemd: the scanned unit behind the trigger
	Pushed        bool
	Scanned       bool
	Inferred      bool
	MutedUntil    time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// TriggerFireRow is one entry of a trigger's fire log.
type TriggerFireRow struct {
	ID              int64
	TriggerID       string
	TS              time.Time
	Target          string
	TargetSessionID string
	Outcome         string
	Line            string
	TextHash        string
	Noop            bool
}

// TriggerStats is the per-trigger aggregate the list view needs.
type TriggerStats struct {
	LastFire *TriggerFireRow
	// LastActive is the newest fire that was not muted: a muted fire says
	// nothing about the trigger's own health.
	LastActive *TriggerFireRow
	Fires24h   int
	Noops24h   int
	FiresByID  map[string]int // target session id -> fires in 24h
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOrZero(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// UpsertPushedTrigger records that a tagged send came from this trigger. It
// keeps every scan-owned column (config path, schedule, unit) and the mute.
func (s *StateDB) UpsertPushedTrigger(row TriggerRow) error {
	now := time.Now().Unix()
	return withBusyRetry(func() error {
		_, err := s.db.Exec(`
			INSERT INTO triggers (id, kind, source, host, verb, target, target_by, target_profile, pushed, inferred, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				host = excluded.host,
				verb = CASE WHEN triggers.verb = '' THEN excluded.verb ELSE triggers.verb END,
				target = CASE WHEN triggers.scanned = 1 AND triggers.target != '' THEN triggers.target ELSE excluded.target END,
				target_by = CASE WHEN triggers.scanned = 1 AND triggers.target != '' THEN triggers.target_by ELSE excluded.target_by END,
				target_profile = CASE WHEN triggers.target_profile = '' THEN excluded.target_profile ELSE triggers.target_profile END,
				pushed = 1,
				inferred = excluded.inferred,
				updated_at = excluded.updated_at
		`, row.ID, row.Kind, row.Source, row.Host, row.Verb, row.Target, row.TargetBy, row.TargetProfile,
			boolInt(row.Inferred), now, now)
		return err
	})
}

// ReplaceScannedTriggers stores the result of one unit scan: every row is
// upserted with scanned=1, and rows the previous scan found but this one did
// not lose their scan flag (and are deleted when they never fired by push).
func (s *StateDB) ReplaceScannedTriggers(rows []TriggerRow) error {
	now := time.Now().Unix()
	return withBusyRetry(func() error {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.Exec(`UPDATE triggers SET scanned = 2 WHERE scanned = 1`); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.Exec(`
				INSERT INTO triggers (id, kind, source, host, config_path, verb, target, target_by, target_profile, schedule, unit, unit_kind, scanned, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
				ON CONFLICT(id) DO UPDATE SET
					kind = excluded.kind, host = excluded.host, config_path = excluded.config_path,
					verb = excluded.verb, target = excluded.target, target_by = excluded.target_by,
					target_profile = excluded.target_profile, schedule = excluded.schedule,
					unit = excluded.unit, unit_kind = excluded.unit_kind, scanned = 1, updated_at = excluded.updated_at
			`, r.ID, r.Kind, r.Source, r.Host, r.ConfigPath, r.Verb, r.Target, r.TargetBy, r.TargetProfile,
				r.Schedule, r.Unit, r.UnitKind, now, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM triggers WHERE scanned = 2 AND pushed = 0`); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE triggers SET scanned = 0, config_path = '', schedule = '', unit = '', unit_kind = '' WHERE scanned = 2`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM trigger_fires WHERE trigger_id NOT IN (SELECT id FROM triggers)`); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// RecordTriggerFire appends one fire and applies the retention bounds. The
// fire counts as a no-op when its text hash equals the previous fire's hash
// for the same trigger (the same message again changes nothing).
func (s *StateDB) RecordTriggerFire(f TriggerFireRow) (TriggerFireRow, error) {
	if f.TS.IsZero() {
		f.TS = time.Now()
	}
	err := withBusyRetry(func() error {
		var prev string
		err := s.db.QueryRow(`SELECT text_hash FROM trigger_fires WHERE trigger_id = ? ORDER BY ts DESC, id DESC LIMIT 1`, f.TriggerID).Scan(&prev)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		f.Noop = f.TextHash != "" && prev == f.TextHash
		res, err := s.db.Exec(`
			INSERT INTO trigger_fires (trigger_id, ts, target, target_session_id, outcome, line, text_hash, noop)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, f.TriggerID, f.TS.Unix(), f.Target, f.TargetSessionID, f.Outcome, f.Line, f.TextHash, boolInt(f.Noop))
		if err != nil {
			return err
		}
		f.ID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		return f, err
	}
	if err := s.pruneTriggerFires(f.TriggerID, f.TS); err != nil {
		return f, err
	}
	return f, s.bumpTriggersRev()
}

func (s *StateDB) pruneTriggerFires(triggerID string, now time.Time) error {
	return withBusyRetry(func() error {
		if _, err := s.db.Exec(`DELETE FROM trigger_fires WHERE ts < ?`, now.Add(-TriggerFireRetention).Unix()); err != nil {
			return err
		}
		_, err := s.db.Exec(`
			DELETE FROM trigger_fires WHERE trigger_id = ? AND id NOT IN (
				SELECT id FROM trigger_fires WHERE trigger_id = ? ORDER BY ts DESC, id DESC LIMIT ?
			)`, triggerID, triggerID, TriggerFireMaxPerTrigger)
		return err
	})
}

// SetTriggerMute mutes a trigger until the given time (zero unmutes). It
// reports whether the trigger exists.
func (s *StateDB) SetTriggerMute(id string, until time.Time) (bool, error) {
	ok, err := s.execAffected(`UPDATE triggers SET muted_until = ?, updated_at = ? WHERE id = ?`,
		unixOrZero(until), time.Now().Unix(), id)
	if err != nil || !ok {
		return ok, err
	}
	return true, s.bumpTriggersRev()
}

// BumpTriggersRev marks the trigger tables changed (scan, mute) for live readers.
func (s *StateDB) BumpTriggersRev() error { return s.bumpTriggersRev() }

func (s *StateDB) bumpTriggersRev() error {
	return withBusyRetry(func() error {
		return s.SetMeta(triggersRevKey, strconv.FormatInt(time.Now().UnixNano(), 10))
	})
}

// TriggersRev returns the last trigger write stamp ("" when never written).
func (s *StateDB) TriggersRev() (string, error) { return s.GetMeta(triggersRevKey) }

const triggerColumns = `id, kind, source, host, config_path, verb, target, target_by, target_profile, schedule, unit, unit_kind,
	pushed, scanned, inferred, muted_until, created_at, updated_at`

func scanTriggerRow(sc interface{ Scan(...any) error }) (TriggerRow, error) {
	var r TriggerRow
	var pushed, scanned, inferred int
	var muted, created, updated int64
	err := sc.Scan(&r.ID, &r.Kind, &r.Source, &r.Host, &r.ConfigPath, &r.Verb, &r.Target, &r.TargetBy,
		&r.TargetProfile, &r.Schedule, &r.Unit, &r.UnitKind, &pushed, &scanned, &inferred, &muted, &created, &updated)
	r.Pushed, r.Scanned, r.Inferred = pushed == 1, scanned == 1, inferred == 1
	r.MutedUntil, r.CreatedAt, r.UpdatedAt = timeOrZero(muted), timeOrZero(created), timeOrZero(updated)
	return r, err
}

// ListTriggers returns every stored trigger, ordered by id.
func (s *StateDB) ListTriggers() ([]TriggerRow, error) {
	rows, err := s.db.Query(`SELECT ` + triggerColumns + ` FROM triggers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TriggerRow
	for rows.Next() {
		r, err := scanTriggerRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetTrigger returns one trigger, or (nil, nil) when it does not exist.
func (s *StateDB) GetTrigger(id string) (*TriggerRow, error) {
	r, err := scanTriggerRow(s.db.QueryRow(`SELECT `+triggerColumns+` FROM triggers WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

const triggerFireColumns = `id, trigger_id, ts, target, target_session_id, outcome, line, text_hash, noop`

func scanTriggerFire(sc interface{ Scan(...any) error }) (TriggerFireRow, error) {
	var f TriggerFireRow
	var ts int64
	var noop int
	err := sc.Scan(&f.ID, &f.TriggerID, &ts, &f.Target, &f.TargetSessionID, &f.Outcome, &f.Line, &f.TextHash, &noop)
	f.TS, f.Noop = time.Unix(ts, 0).UTC(), noop == 1
	return f, err
}

// ListTriggerFires returns the newest fires of one trigger, newest first.
func (s *StateDB) ListTriggerFires(triggerID string, limit int) ([]TriggerFireRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+triggerFireColumns+` FROM trigger_fires WHERE trigger_id = ? ORDER BY ts DESC, id DESC LIMIT ?`, triggerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TriggerFireRow
	for rows.Next() {
		f, err := scanTriggerFire(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// TriggerStatsSince aggregates the fire log for every trigger: the last fire,
// and fires / no-op fires since the given time.
func (s *StateDB) TriggerStatsSince(since time.Time) (map[string]*TriggerStats, error) {
	out := map[string]*TriggerStats{}
	get := func(id string) *TriggerStats {
		st := out[id]
		if st == nil {
			st = &TriggerStats{FiresByID: map[string]int{}}
			out[id] = st
		}
		return st
	}
	rows, err := s.db.Query(`
		SELECT trigger_id, target_session_id, COUNT(*), SUM(noop) FROM trigger_fires
		WHERE ts >= ? GROUP BY trigger_id, target_session_id`, since.Unix())
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, sid string
		var n, noops int
		if err := rows.Scan(&id, &sid, &n, &noops); err != nil {
			rows.Close()
			return nil, err
		}
		st := get(id)
		st.Fires24h += n
		st.Noops24h += noops
		if sid != "" {
			st.FiresByID[sid] += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	active, err := s.db.Query(`
		SELECT ` + triggerFireColumns + ` FROM trigger_fires f
		WHERE f.id = (SELECT g.id FROM trigger_fires g WHERE g.trigger_id = f.trigger_id AND g.outcome != 'muted' ORDER BY g.ts DESC, g.id DESC LIMIT 1)`)
	if err != nil {
		return nil, err
	}
	for active.Next() {
		f, err := scanTriggerFire(active)
		if err != nil {
			active.Close()
			return nil, err
		}
		fc := f
		get(f.TriggerID).LastActive = &fc
	}
	active.Close()
	if err := active.Err(); err != nil {
		return nil, err
	}
	last, err := s.db.Query(`
		SELECT ` + triggerFireColumns + ` FROM trigger_fires f
		WHERE f.id = (SELECT g.id FROM trigger_fires g WHERE g.trigger_id = f.trigger_id ORDER BY g.ts DESC, g.id DESC LIMIT 1)`)
	if err != nil {
		return nil, err
	}
	defer last.Close()
	for last.Next() {
		f, err := scanTriggerFire(last)
		if err != nil {
			return nil, err
		}
		fc := f
		get(f.TriggerID).LastFire = &fc
	}
	return out, last.Err()
}
