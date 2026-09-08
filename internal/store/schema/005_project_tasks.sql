CREATE TABLE project_tasks (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 room_id TEXT NOT NULL REFERENCES rooms(id), number INTEGER NOT NULL,
 source_message_id INTEGER NOT NULL, source_seq INTEGER NOT NULL,
 title TEXT NOT NULL, acceptance TEXT NOT NULL DEFAULT '',
 revision INTEGER NOT NULL DEFAULT 1,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 completed_at TEXT NOT NULL DEFAULT '', completed_by TEXT NOT NULL DEFAULT '',
 UNIQUE(room_id,id), UNIQUE(room_id,number), UNIQUE(room_id,source_message_id),
 FOREIGN KEY(room_id,source_message_id) REFERENCES messages(room_id,id)
);
CREATE TABLE task_messages (
 room_id TEXT NOT NULL, task_id INTEGER NOT NULL, message_id INTEGER NOT NULL,
 PRIMARY KEY(room_id,task_id,message_id), UNIQUE(room_id,message_id),
 FOREIGN KEY(room_id,task_id) REFERENCES project_tasks(room_id,id),
 FOREIGN KEY(room_id,message_id) REFERENCES messages(room_id,id)
);
CREATE TABLE task_changes (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 room_id TEXT NOT NULL, task_id INTEGER NOT NULL,
 action TEXT NOT NULL, actor TEXT NOT NULL, created_at TEXT NOT NULL, note TEXT NOT NULL DEFAULT '',
 FOREIGN KEY(room_id,task_id) REFERENCES project_tasks(room_id,id)
);
CREATE TABLE task_receipts (
 room_id TEXT NOT NULL, uid INTEGER NOT NULL, request_id TEXT NOT NULL,
 task_id INTEGER NOT NULL, payload_hash BLOB NOT NULL,
 PRIMARY KEY(room_id,uid,request_id), FOREIGN KEY(room_id,task_id) REFERENCES project_tasks(room_id,id)
);
CREATE INDEX task_changes_by_task ON task_changes(room_id,task_id,id);
UPDATE rooms SET schema_version=5;
