CREATE TABLE rooms (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  host_id TEXT NOT NULL,
  project_path TEXT NOT NULL,
  execution_owner_uid INTEGER NOT NULL,
  codex_thread_id TEXT,
  status TEXT NOT NULL CHECK (status IN ('ready','recovering','thread-needs-repair')),
  next_seq INTEGER NOT NULL DEFAULT 1 CHECK (next_seq >= 1),
  schema_version INTEGER NOT NULL
);

CREATE TABLE members (
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  uid INTEGER NOT NULL,
  username TEXT NOT NULL,
  added_at TEXT NOT NULL,
  PRIMARY KEY (room_id, uid),
  UNIQUE (room_id, username)
);

CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  client_message_id TEXT NOT NULL,
  actor_uid INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('prompt','note','steer','cancel','recover','recovery-prompt')),
  body TEXT NOT NULL,
  payload_hash BLOB NOT NULL CHECK (length(payload_hash) = 32),
  state TEXT NOT NULL CHECK (state IN ('queued','dispatching','running','completed','failed','interrupted','needs-review')),
  accepted_seq INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (room_id, client_message_id),
  FOREIGN KEY (room_id, actor_uid) REFERENCES members(room_id, uid)
);

CREATE TABLE turn_bindings (
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  message_id INTEGER NOT NULL UNIQUE REFERENCES messages(id) ON DELETE CASCADE,
  codex_turn_id TEXT,
  state TEXT NOT NULL CHECK (state IN ('queued','dispatching','running','completed','failed','interrupted','needs-review')),
  started_at TEXT,
  completed_at TEXT,
  error_code TEXT,
  error_digest TEXT,
  PRIMARY KEY (room_id, message_id)
);

CREATE TABLE client_connections (
  id TEXT PRIMARY KEY,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  member_uid INTEGER NOT NULL,
  connected_at TEXT NOT NULL,
  disconnected_at TEXT,
  last_ack_seq INTEGER NOT NULL DEFAULT 0 CHECK (last_ack_seq >= 0),
  FOREIGN KEY (room_id, member_uid) REFERENCES members(room_id, uid)
);

CREATE TABLE room_events (
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  actor_uid INTEGER NOT NULL,
  kind TEXT NOT NULL,
  payload BLOB NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (room_id, seq)
);

CREATE TABLE runtime_checkpoints (
  room_id TEXT PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
  generation TEXT NOT NULL,
  pid INTEGER NOT NULL,
  pgid INTEGER NOT NULL,
  process_start TEXT NOT NULL,
  codex_version TEXT NOT NULL,
  schema_sha256 TEXT NOT NULL,
  process_state TEXT NOT NULL CHECK (process_state IN ('running','stopped'))
);

CREATE TABLE recovery_results (
  recovery_message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  target_message_id INTEGER NOT NULL REFERENCES messages(id),
  action TEXT NOT NULL CHECK (action IN ('retry','skip','continue'))
);

CREATE TABLE recovery_result_events (
  recovery_message_id INTEGER NOT NULL REFERENCES recovery_results(recovery_message_id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL,
  room_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  PRIMARY KEY (recovery_message_id, ordinal),
  FOREIGN KEY (room_id, seq) REFERENCES room_events(room_id, seq)
);

CREATE INDEX messages_fifo ON messages(room_id, state, accepted_seq);
CREATE INDEX bindings_active ON turn_bindings(room_id, state, codex_turn_id);
CREATE INDEX connections_online ON client_connections(room_id, disconnected_at);
