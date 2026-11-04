CREATE TABLE runs (
    id          TEXT PRIMARY KEY,
    started_at  INTEGER NOT NULL,
    ended_at    INTEGER,
    end_reason  TEXT,
    app_version TEXT NOT NULL
);

CREATE TABLE agents (
    id              TEXT PRIMARY KEY,
    run_id          TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    display_name    TEXT NOT NULL,
    harness         TEXT NOT NULL,
    harness_version TEXT NOT NULL DEFAULT '',
    started_at      INTEGER NOT NULL,
    exited_at       INTEGER,
    exit_code       INTEGER
);

CREATE TABLE messages (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    id            TEXT NOT NULL UNIQUE,
    run_id        TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    from_agent_id TEXT NOT NULL,
    to_agent_id   TEXT NOT NULL,
    reply_to      TEXT NOT NULL DEFAULT '',
    body          TEXT NOT NULL,
    state         TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE INDEX messages_run_seq ON messages(run_id, seq);

CREATE TABLE message_events (
    id         INTEGER PRIMARY KEY,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    at         INTEGER NOT NULL,
    from_state TEXT NOT NULL,
    to_state   TEXT NOT NULL,
    reason     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX message_events_message ON message_events(message_id, id);
