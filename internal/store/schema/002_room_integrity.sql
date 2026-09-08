CREATE UNIQUE INDEX messages_room_id_id_unique ON messages(room_id, id);

CREATE TABLE turn_bindings_v2 (
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  message_id INTEGER NOT NULL UNIQUE,
  codex_turn_id TEXT,
  state TEXT NOT NULL CHECK (state IN ('queued','dispatching','running','completed','failed','interrupted','needs-review')),
  started_at TEXT,
  completed_at TEXT,
  error_code TEXT,
  error_digest TEXT,
  PRIMARY KEY (room_id, message_id),
  FOREIGN KEY (room_id, message_id) REFERENCES messages(room_id, id) ON DELETE CASCADE
);

INSERT INTO turn_bindings_v2(
  room_id, message_id, codex_turn_id, state, started_at, completed_at, error_code, error_digest
)
SELECT room_id, message_id, codex_turn_id, state, started_at, completed_at, error_code, error_digest
FROM turn_bindings;

CREATE TABLE recovery_results_v2 (
  recovery_message_id INTEGER PRIMARY KEY,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  target_message_id INTEGER NOT NULL,
  action TEXT NOT NULL CHECK (action IN ('retry','skip','continue')),
  UNIQUE (room_id, recovery_message_id),
  FOREIGN KEY (room_id, recovery_message_id) REFERENCES messages(room_id, id) ON DELETE CASCADE,
  FOREIGN KEY (room_id, target_message_id) REFERENCES messages(room_id, id)
);

INSERT INTO recovery_results_v2(recovery_message_id, room_id, target_message_id, action)
SELECT recovery_message_id, room_id, target_message_id, action
FROM recovery_results;

CREATE TABLE recovery_result_events_v2 (
  recovery_message_id INTEGER NOT NULL,
  ordinal INTEGER NOT NULL,
  room_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  PRIMARY KEY (recovery_message_id, ordinal),
  FOREIGN KEY (room_id, recovery_message_id) REFERENCES recovery_results_v2(room_id, recovery_message_id) ON DELETE CASCADE,
  FOREIGN KEY (room_id, seq) REFERENCES room_events(room_id, seq)
);

INSERT INTO recovery_result_events_v2(recovery_message_id, ordinal, room_id, seq)
SELECT recovery_message_id, ordinal, room_id, seq
FROM recovery_result_events;

DROP TABLE recovery_result_events;
DROP TABLE recovery_results;
DROP TABLE turn_bindings;

ALTER TABLE recovery_results_v2 RENAME TO recovery_results;
ALTER TABLE recovery_result_events_v2 RENAME TO recovery_result_events;
ALTER TABLE turn_bindings_v2 RENAME TO turn_bindings;

CREATE INDEX bindings_active ON turn_bindings(room_id, state, codex_turn_id);

UPDATE rooms SET schema_version = 2;
