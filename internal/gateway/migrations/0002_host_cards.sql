-- Host cards: where to poll, what was last seen, the older good cards, and the poll attempts.
--
-- Additive only: no change to hosts, pairing_codes, sessions, messages or message_events. Every
-- table below hangs off hosts(name) with ON DELETE CASCADE, so the one DELETE FROM hosts that an
-- unpair already does removes a host's target, cards, revisions and attempts in the same
-- transaction (foreign keys are on in the connection string).
--
-- A stored card is a record of what the gateway last saw, never proof of anything live. Poll
-- results describe the card endpoint only; the heartbeat and stream stay the authority on whether a
-- host is connected, and nothing here feeds them.
--
-- Times are Unix milliseconds.

CREATE TABLE host_card_targets (
  host TEXT PRIMARY KEY REFERENCES hosts(name) ON DELETE CASCADE,
  scheme TEXT NOT NULL CHECK (scheme IN ('http', 'https')),
  addr TEXT NOT NULL,
  class TEXT NOT NULL CHECK (class IN ('loopback', 'tailscale', 'lan')),
  fingerprint TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE host_cards (
  host TEXT PRIMARY KEY REFERENCES hosts(name) ON DELETE CASCADE,
  card_json TEXT NOT NULL DEFAULT '',
  revision TEXT NOT NULL DEFAULT '',
  etag TEXT NOT NULL DEFAULT '',
  hash TEXT NOT NULL DEFAULT '',
  unknown_fields INTEGER NOT NULL DEFAULT 0,
  fetched_at INTEGER,
  last_ok_at INTEGER,
  last_attempt_at INTEGER,
  last_result TEXT NOT NULL DEFAULT '',
  last_reason TEXT NOT NULL DEFAULT ''
);

CREATE TABLE card_revisions (
  id INTEGER PRIMARY KEY,
  host TEXT NOT NULL REFERENCES hosts(name) ON DELETE CASCADE,
  revision TEXT NOT NULL,
  etag TEXT NOT NULL,
  hash TEXT NOT NULL,
  card_json TEXT NOT NULL,
  fetched_at INTEGER NOT NULL
);

CREATE INDEX card_revisions_host ON card_revisions(host, id);

CREATE TABLE poll_attempts (
  id INTEGER PRIMARY KEY,
  host TEXT NOT NULL REFERENCES hosts(name) ON DELETE CASCADE,
  at INTEGER NOT NULL,
  outcome TEXT NOT NULL,
  http_status INTEGER,
  duration_ms INTEGER NOT NULL,
  reason TEXT NOT NULL DEFAULT ''
);

CREATE INDEX poll_attempts_host ON poll_attempts(host, id);
