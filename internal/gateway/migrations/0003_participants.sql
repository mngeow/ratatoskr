-- Participants: external adapters that use the participant API (docs/implementation/participant-api-contract.md).
--
-- Additive only: no change to hosts, pairing_codes, sessions, messages or message_events. A participant
-- session is a row in the existing sessions table whose host column holds the participant id; that is safe
-- because a participant id and a host name can never be equal (the pairing paths enforce it, and redemption
-- re-checks inside the transaction that writes the row).
--
-- A revoked participant row is kept: its history and its name stay reserved.
--
-- Never run against any database. Times are Unix milliseconds.

CREATE TABLE participants (
  id TEXT PRIMARY KEY,
  credential_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_poll_at INTEGER,
  revoked_at INTEGER
);

CREATE TABLE participant_pairing_codes (
  code_hash TEXT PRIMARY KEY,
  participant TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at INTEGER
);
