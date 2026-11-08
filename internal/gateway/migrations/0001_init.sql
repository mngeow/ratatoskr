-- Migration for the gateway database: hosts, pairing codes, sessions, the
-- message ledger and its events.
--
-- This is schema version 1. A gateway database at this version now exists on users' machines, so
-- it is never edited again: every later change is its own numbered file (0002, 0003, ...), applied
-- in order by the migrator in migrate.go after a backup. The session columns on messages were added
-- here, before any database existed, which is why there is no separate file for them.
--
-- Times are Unix milliseconds. There is no retention or deletion code anywhere:
-- the ledger is append-only apart from the state columns the routing slice
-- updates.

CREATE TABLE hosts (
  name TEXT PRIMARY KEY,
  credential_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_heartbeat_at INTEGER
);

CREATE TABLE pairing_codes (
  code_hash TEXT PRIMARY KEY,
  host TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at INTEGER
);

CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  host TEXT NOT NULL,
  name TEXT NOT NULL,
  started_at INTEGER NOT NULL,
  ended_at INTEGER
);

CREATE INDEX sessions_host ON sessions(host, ended_at);

CREATE TABLE messages (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  from_address TEXT NOT NULL,
  to_address TEXT NOT NULL,
  reply_to TEXT NOT NULL DEFAULT '',
  from_session TEXT NOT NULL DEFAULT '',
  to_session TEXT NOT NULL DEFAULT '',
  result_reason TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL,
  result TEXT NOT NULL,
  report TEXT NOT NULL DEFAULT '',
  report_reason TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE message_events (
  id INTEGER PRIMARY KEY,
  message_id TEXT NOT NULL REFERENCES messages(id),
  at INTEGER NOT NULL,
  kind TEXT NOT NULL,
  value TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT ''
);

CREATE INDEX message_events_message ON message_events(message_id, id);
