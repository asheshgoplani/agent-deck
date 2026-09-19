// Package recall holds the schema of recall.db, agent-deck's cross-harness
// conversation store (docs/recall.md), and the paths it lives at.
//
// The DDL is pinned by a build-time test (schema_test.go) that executes it
// against modernc.org/sqlite and asserts the FTS5 facts the design depends
// on. Durable human intent (hints, tags, links) is NOT here; it lives in
// state.db (internal/statedb/recall_hints.go) because recall.db is
// disposable and a rebuild must never lose it. The layers built on this
// schema are the subpackages: reader (harness files to events), ingest (the
// only writer), query (the read side), classify (message classes), store
// (handles, pragmas, the sweep lock).
package recall

// FTSTokenizer is shared by every FTS5 table: unicode61 with diacritics
// folded and '_', '-', '.' kept inside tokens so identifiers such as
// handle_sess, agent-deck and state.db survive as one term.
const FTSTokenizer = `tokenize="unicode61 remove_diacritics 2 tokenchars '_-.'"` //nolint:gosec // G101: FTS5 tokenizer DDL option, not a credential — the constant name matching gosec's "token" heuristic is coincidental

// MsgFTSDDL is the message body index: contentless (bodies live compressed
// in msg.body), with per-rowid delete so a vanished source can be reclaimed,
// and detail=none because it is a membership filter only. It cannot rank,
// snippet or run phrase queries; all ordering happens in SQL. columnsize=0
// is deliberately absent: SQLite rejects it together with contentless_delete.
const MsgFTSDDL = `CREATE VIRTUAL TABLE msg_fts USING fts5(
  body, content='', contentless_delete=1, detail=none,
  ` + FTSTokenizer + `
)`

// CardDDL is the one ranked, phrase-capable surface: an external-content
// FTS5 table over card, kept in sync by the three triggers. Without the
// triggers an external-content table silently matches nothing.
const CardDDL = `CREATE TABLE card(
  sess_id INTEGER PRIMARY KEY,
  title TEXT, hints TEXT, tags TEXT, summary TEXT, preview TEXT
)`

// CardFTSDDL indexes card; rowid is the session id.
const CardFTSDDL = `CREATE VIRTUAL TABLE card_fts USING fts5(
  title, hints, tags, summary, preview,
  content='card', content_rowid='sess_id',
  ` + FTSTokenizer + `
)`

// CardTriggerDDL keeps card_fts in step with card: insert, delete and the
// delete-then-insert pair on update that drops the stale posting.
var CardTriggerDDL = []string{
	`CREATE TRIGGER card_ai AFTER INSERT ON card BEGIN
  INSERT INTO card_fts(rowid,title,hints,tags,summary,preview)
  VALUES (new.sess_id,new.title,new.hints,new.tags,new.summary,new.preview); END`,
	`CREATE TRIGGER card_ad AFTER DELETE ON card BEGIN
  INSERT INTO card_fts(card_fts,rowid,title,hints,tags,summary,preview)
  VALUES ('delete',old.sess_id,old.title,old.hints,old.tags,old.summary,old.preview); END`,
	`CREATE TRIGGER card_au AFTER UPDATE ON card BEGIN
  INSERT INTO card_fts(card_fts,rowid,title,hints,tags,summary,preview)
  VALUES ('delete',old.sess_id,old.title,old.hints,old.tags,old.summary,old.preview);
  INSERT INTO card_fts(rowid,title,hints,tags,summary,preview)
  VALUES (new.sess_id,new.title,new.hints,new.tags,new.summary,new.preview); END`,
}

// TextClipBytes is the default per-message body clip (text_tier=clipped).
const TextClipBytes = 8 * 1024

// SchemaVersion is recorded in meta(k='schema_version'). A mismatch on open
// means the file was written by a different phase: the store drops and
// recreates it, which is exactly what "disposable" means.
const SchemaVersion = 2

// ReaderVersion is stamped on every source row. Bumping it forces a full
// reparse of every source on the next sweep without dropping the database.
const ReaderVersion = 1

// Source states (source.state).
const (
	SourceOK          = 0
	SourcePartial     = 1
	SourceError       = 2
	SourceMissing     = 3
	SourceQuarantined = 4
)

// Message roles (msg.role).
const (
	RoleUser      = 1
	RoleAssistant = 2
)

// TableDDL is every ordinary table and index of recall.db, in creation
// order; the FTS surfaces and their triggers follow (MsgFTSDDL, CardDDL,
// CardFTSDDL, CardTriggerDDL). AllDDL returns the whole list.
var TableDDL = []string{
	`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
	`CREATE TABLE host(
  host_uid TEXT PRIMARY KEY,
  alias    TEXT NOT NULL DEFAULT '',
  is_local INTEGER NOT NULL,
  last_seen INTEGER NOT NULL DEFAULT 0,
  CHECK (is_local IN (0,1)),
  CHECK (is_local = 1 OR length(host_uid) > 0)
)`,
	`CREATE TABLE source(
  src_id      INTEGER PRIMARY KEY,
  host_uid    TEXT NOT NULL REFERENCES host(host_uid),
  harness     TEXT NOT NULL,
  profile     TEXT NOT NULL DEFAULT '',
  path        TEXT NOT NULL,
  dev         INTEGER NOT NULL DEFAULT 0,
  ino         INTEGER NOT NULL DEFAULT 0,
  size        INTEGER NOT NULL DEFAULT 0,
  mtime_ns    INTEGER NOT NULL DEFAULT 0,
  parsed_to   INTEGER NOT NULL DEFAULT 0,
  prefix_sig  TEXT NOT NULL DEFAULT '',
  tail_sig    TEXT NOT NULL DEFAULT '',
  reader_ver  INTEGER NOT NULL DEFAULT 1,
  retention_d INTEGER NOT NULL DEFAULT 0,
  state       INTEGER NOT NULL DEFAULT 0,
  last_error  TEXT NOT NULL DEFAULT '',
  last_seen   INTEGER NOT NULL DEFAULT 0,
  sess_id     INTEGER NOT NULL DEFAULT 0,
  UNIQUE(host_uid, dev, ino)
)`,
	`CREATE INDEX idx_source_path ON source(host_uid, path)`,
	`CREATE TABLE session(
  sess_id INTEGER PRIMARY KEY,
  host_uid TEXT NOT NULL REFERENCES host(host_uid),
  harness TEXT NOT NULL, profile TEXT NOT NULL DEFAULT '', native_id TEXT NOT NULL,
  deck_id TEXT NOT NULL DEFAULT '',
  cwd TEXT DEFAULT '', project_key TEXT DEFAULT '', repo TEXT DEFAULT '', branch TEXT DEFAULT '',
  title TEXT DEFAULT '', title_src TEXT DEFAULT '',
  started_at INTEGER, ended_at INTEGER,
  turns INTEGER DEFAULT 0, tool_calls INTEGER DEFAULT 0,
  errors INTEGER DEFAULT 0, interrupts INTEGER DEFAULT 0, compacts INTEGER DEFAULT 0,
  in_tok INTEGER DEFAULT 0, out_tok INTEGER DEFAULT 0,
  cache_r INTEGER DEFAULT 0, cache_w INTEGER DEFAULT 0, reason_tok INTEGER DEFAULT 0,
  cost_usd REAL DEFAULT 0, model TEXT DEFAULT '', is_sidechain INTEGER DEFAULT 0,
  text_tier TEXT NOT NULL DEFAULT 'clipped',
  digest_only INTEGER NOT NULL DEFAULT 0,
  derived_rev INTEGER NOT NULL DEFAULT 0,
  UNIQUE(host_uid, harness, profile, native_id)
)`,
	`CREATE INDEX idx_session_time ON session(started_at DESC)`,
	`CREATE INDEX idx_session_proj ON session(project_key, started_at DESC)`,
	`CREATE TABLE msg(
  msg_id INTEGER PRIMARY KEY,
  sess_id INTEGER NOT NULL, src_id INTEGER NOT NULL,
  seq INTEGER NOT NULL, role INTEGER NOT NULL, class INTEGER NOT NULL, ts INTEGER NOT NULL,
  rec_off INTEGER NOT NULL, rec_len INTEGER NOT NULL,
  spanv BLOB,
  nchars INTEGER NOT NULL DEFAULT 0,
  tool_name TEXT NOT NULL DEFAULT '',
  is_error INTEGER NOT NULL DEFAULT 0, is_interrupt INTEGER NOT NULL DEFAULT 0,
  superseded INTEGER NOT NULL DEFAULT 0,
  body BLOB,
  UNIQUE(sess_id, seq)
)`,
	`CREATE INDEX idx_msg_sess ON msg(sess_id, seq)`,
	`CREATE INDEX idx_msg_src  ON msg(src_id, rec_off)`,
	`CREATE TABLE tool_call(
  call_id INTEGER PRIMARY KEY,
  sess_id INTEGER NOT NULL, src_id INTEGER NOT NULL,
  name TEXT NOT NULL, ts INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  is_error INTEGER NOT NULL DEFAULT 0,
  arg_digest TEXT NOT NULL DEFAULT ''
)`,
	`CREATE INDEX idx_tool_call_sess ON tool_call(sess_id, ts)`,
	`CREATE TABLE artifact(
  art_id INTEGER PRIMARY KEY,
  sess_id INTEGER NOT NULL, msg_id INTEGER,
  kind TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '', body_json TEXT NOT NULL DEFAULT '',
  producer TEXT NOT NULL, producer_ver TEXT NOT NULL DEFAULT '1',
  confidence REAL NOT NULL DEFAULT 1.0,
  input_rev INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  UNIQUE(sess_id, kind, producer, producer_ver)
)`,
	`CREATE TABLE enrich_queue(
  sess_id INTEGER NOT NULL, kind TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 100,
  cost_class TEXT NOT NULL DEFAULT 'free',
  state TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
  not_before INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(sess_id, kind)
)`,
	`CREATE INDEX idx_enrich_drain ON enrich_queue(cost_class, state, not_before, priority)`,
	`CREATE TABLE file_touch(
  sess_id INTEGER NOT NULL, path TEXT NOT NULL, op TEXT NOT NULL,
  n INTEGER NOT NULL DEFAULT 1, first_ts INTEGER, last_ts INTEGER,
  PRIMARY KEY(sess_id, path, op)
)`,
	`CREATE INDEX idx_file_touch_path ON file_touch(path)`,
	`CREATE TABLE conv_edge(
  from_sess INTEGER NOT NULL, to_sess INTEGER NOT NULL, kind TEXT NOT NULL,
  weight REAL NOT NULL DEFAULT 1.0, created_at INTEGER NOT NULL,
  PRIMARY KEY(from_sess, to_sess, kind)
)`,
	`CREATE TABLE tombstone(src_id INTEGER, sess_id INTEGER, reason TEXT, at INTEGER)`,
	`CREATE TABLE remote_sync(host_uid TEXT PRIMARY KEY, alias TEXT, last_pull INTEGER,
  cursor TEXT, sessions INTEGER, status TEXT, err TEXT)`,
}

// AllDDL returns every statement that creates an empty recall.db.
func AllDDL() []string {
	out := make([]string, 0, len(TableDDL)+3+len(CardTriggerDDL))
	out = append(out, TableDDL...)
	out = append(out, MsgFTSDDL, CardDDL, CardFTSDDL)
	out = append(out, CardTriggerDDL...)
	return out
}
