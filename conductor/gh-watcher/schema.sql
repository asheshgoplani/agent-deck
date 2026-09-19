-- gh-watcher schema. Lives in its own SQLite file (db_path in watcher.toml).
PRAGMA journal_mode=WAL;

CREATE TABLE IF NOT EXISTS gh_events_seen (
  event_id       TEXT PRIMARY KEY,
  created_at     TEXT NOT NULL,
  event_type     TEXT NOT NULL,
  subtype        TEXT,
  delivery_class TEXT NOT NULL,
  payload_json   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS gh_poll_cursor (
  name       TEXT PRIMARY KEY,
  etag       TEXT,
  last_id    TEXT,
  since      TEXT,
  updated_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS gh_deliver_queue (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  enqueued_at TEXT NOT NULL,
  event_id    TEXT NOT NULL,
  parent_key  TEXT,
  priority    INTEGER NOT NULL,
  items_count INTEGER DEFAULT 1,
  message     TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'pending',
  sent_at     TEXT,
  UNIQUE(event_id, parent_key)
);

CREATE INDEX IF NOT EXISTS ix_queue_pending
  ON gh_deliver_queue(status, priority, enqueued_at);

CREATE TABLE IF NOT EXISTS gh_digest (
  class        TEXT NOT NULL,
  window_start TEXT NOT NULL,
  event_id     TEXT NOT NULL,
  summary      TEXT NOT NULL,
  PRIMARY KEY (class, event_id)
);

CREATE TABLE IF NOT EXISTS gh_dispatch_state (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
