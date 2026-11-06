-- Migration for the gateway database: hosts, pairing codes, sessions, the
-- message ledger and its events.
--
-- This schema has never been created anywhere: nothing has run under GR-3, so
-- there is no deployed version 1 to migrate. That is why the session columns on
-- messages are added here rather than in a second migration file; if a gateway
-- database ever exists before a later schema change, that change gets its own
-- numbered file and this comment stops being true.
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
