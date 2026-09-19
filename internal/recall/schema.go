// Package recall holds the schema of recall.db, agent-deck's cross-harness
// conversation store (docs/recall.md).
//
// Phase 1 ships no ingestion, no reader and no query path: this package is
// the DDL the later phases build on, pinned by a build-time test
// (schema_test.go) that executes it against modernc.org/sqlite and asserts
// the FTS5 facts the design depends on. Durable human intent (hints, tags,
// links) is NOT here; it lives in state.db (internal/statedb/recall_hints.go)
// because recall.db is disposable and a rebuild must never lose it.
package recall

// FTSTokenizer is shared by every FTS5 table: unicode61 with diacritics
// folded and '_', '-', '.' kept inside tokens so identifiers such as
// handle_sess, agent-deck and state.db survive as one term.
const FTSTokenizer = `tokenize="unicode61 remove_diacritics 2 tokenchars '_-.'"`

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
