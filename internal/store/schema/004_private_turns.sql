CREATE TABLE private_turns (
 room_id TEXT NOT NULL REFERENCES rooms(id), thread_id TEXT NOT NULL, turn_id TEXT NOT NULL,
 PRIMARY KEY(room_id,thread_id,turn_id)
);
CREATE TABLE private_turn_frontiers (
 room_id TEXT PRIMARY KEY REFERENCES rooms(id), thread_id TEXT NOT NULL, baseline TEXT NOT NULL
);
UPDATE rooms SET schema_version=4;
