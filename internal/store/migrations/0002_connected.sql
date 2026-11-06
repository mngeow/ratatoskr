-- Cross-machine slice 2: outbound attempts, their events, and the inbound
-- origin/report state. Additive only: no existing table or column is changed,
-- and every statement here is unaffected by a database that never runs connected.
--
-- Times are Unix milliseconds. kind in send_events is "result" or "report".

CREATE TABLE sends (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    id            TEXT NOT NULL UNIQUE,
    run_id        TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    from_agent_id TEXT NOT NULL,
    to_address    TEXT NOT NULL,
    reply_to      TEXT NOT NULL DEFAULT '',
    body          TEXT NOT NULL,
    result        TEXT NOT NULL,
    report        TEXT NOT NULL DEFAULT '',
    report_reason TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE INDEX sends_run_seq ON sends(run_id, seq);

CREATE TABLE send_events (
    id      INTEGER PRIMARY KEY,
    send_id TEXT NOT NULL REFERENCES sends(id) ON DELETE CASCADE,
    at      INTEGER NOT NULL,
    kind    TEXT NOT NULL,
    value   TEXT NOT NULL,
    reason  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX send_events_send ON send_events(send_id, id);

CREATE TABLE inbound (
    message_id     TEXT PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    origin         TEXT NOT NULL,
    reported_state TEXT NOT NULL DEFAULT ''
);
