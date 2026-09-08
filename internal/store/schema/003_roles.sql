CREATE TABLE member_roles (
 room_id TEXT NOT NULL, uid INTEGER NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('roommate','visitor','asker')),
 PRIMARY KEY(room_id,uid), FOREIGN KEY(room_id,uid) REFERENCES members(room_id,uid)
);
UPDATE rooms SET schema_version=3;
