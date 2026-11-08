-- A2A participants: enrolled external principals, tasks and their parties, and observations.
--
-- Additive only: no change to any existing table. NOTHING here references hosts or sessions, on purpose:
-- tasks are kept until the owner deletes them (contract section 2, rule 9), so unpairing a host or ending
-- a session must never cascade into them. A session principal is stored as text ("session:<host>/<id>").
--
-- Credentials are stored as SHA-256 hashes. Times are Unix milliseconds.
--
-- a2a_observations and a2a_dispatches carry a task_id WITHOUT a foreign key: the executor yields a task and
-- the SDK writes its row asynchronously, so a dispatch or an observation can be recorded a moment first.
-- DeleteTasks removes their rows explicitly.

CREATE TABLE a2a_participants (
  id TEXT PRIMARY KEY,
  credential_hash TEXT NOT NULL UNIQUE,
  url TEXT NOT NULL DEFAULT '',
  fingerprint TEXT NOT NULL DEFAULT '',
  insecure_http INTEGER NOT NULL DEFAULT 0,
  card_json TEXT NOT NULL DEFAULT '',
  -- How the gateway authenticates to the registered service: a per-registration Bearer secret, or none
  -- (the explicit demo mode). outbound_secret is selected ONLY by the outbound-client factory and never
  -- appears in a list, a log, an error, an observation, a card or any API JSON.
  auth TEXT NOT NULL DEFAULT 'bearer' CHECK (auth IN ('bearer', 'none')),
  outbound_secret TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  revoked_at INTEGER
);

CREATE TABLE a2a_tasks (
  id TEXT PRIMARY KEY,
  seq INTEGER NOT NULL,
  context_id TEXT NOT NULL,
  owner TEXT NOT NULL,
  target_kind TEXT NOT NULL CHECK (target_kind IN ('session', 'service')),
  target_id TEXT NOT NULL,
  target_host TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  remote_task_id TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL,
  version INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX a2a_tasks_seq ON a2a_tasks(seq);
CREATE INDEX a2a_tasks_owner ON a2a_tasks(owner, seq);
CREATE INDEX a2a_tasks_target ON a2a_tasks(target_kind, target_id, seq);
CREATE INDEX a2a_tasks_state ON a2a_tasks(state);

CREATE TABLE a2a_observations (
  id INTEGER PRIMARY KEY,
  task_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  value TEXT NOT NULL,
  at INTEGER NOT NULL
);

CREATE INDEX a2a_observations_task ON a2a_observations(task_id, id);

-- Which gateway ledger messages carry an A2A task: the dispatch to a managed target and the wake sent to a
-- managed owner. The link lets a delivery receipt on that message become an observation (and, for
-- "accepted", the WORKING state), and lets the owner's delete command remove the ledger rows too.
-- message_id is the id of a row in messages; it is not a foreign key so a ledger prune could never block
-- or cascade into tasks.
CREATE TABLE a2a_dispatches (
  message_id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('dispatch', 'wake')),
  created_at INTEGER NOT NULL
);

CREATE INDEX a2a_dispatches_task ON a2a_dispatches(task_id);
