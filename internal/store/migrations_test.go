package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenUpgradesRealVersionOneDatabaseWithoutDataLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-one.db")
	fixture := createVersionOneFixture(t, path)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	assertMigrationVersion(t, s, 2, 2)
	var roomVersion int
	if err := s.db.QueryRow("SELECT schema_version FROM rooms WHERE id = 'team'").Scan(&roomVersion); err != nil {
		t.Fatal(err)
	}
	if roomVersion != 2 {
		t.Fatalf("room schema version=%d", roomVersion)
	}
	wantCounts := map[string]int{
		"rooms":                  1,
		"members":                1,
		"messages":               2,
		"turn_bindings":          1,
		"recovery_results":       1,
		"recovery_result_events": 1,
		"room_events":            2,
		"runtime_checkpoints":    1,
		"client_connections":     1,
	}
	for table, want := range wantCounts {
		if got := tableCount(t, s, table); got != want {
			t.Fatalf("%s rows=%d want=%d", table, got, want)
		}
	}
	var body string
	if err := s.db.QueryRow("SELECT body FROM messages WHERE id = ?", fixture.promptMessageID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "persist me" {
		t.Fatalf("prompt body=%q", body)
	}
	var action, processState string
	var ordinal, pid, pgid, lastAck int
	if err := s.db.QueryRow("SELECT action FROM recovery_results").Scan(&action); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT ordinal FROM recovery_result_events").Scan(&ordinal); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT process_state, pid, pgid FROM runtime_checkpoints").Scan(&processState, &pid, &pgid); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT last_ack_seq FROM client_connections").Scan(&lastAck); err != nil {
		t.Fatal(err)
	}
	if action != "skip" || ordinal != 0 || processState != "stopped" || pid != 0 || pgid != 0 || lastAck != 2 {
		t.Fatalf("migration changed representative data: action=%q ordinal=%d process=%q pid=%d pgid=%d ack=%d", action, ordinal, processState, pid, pgid, lastAck)
	}
	otherMessageID := insertOtherRoomMessage(t, s)
	if _, err := s.db.Exec("INSERT INTO turn_bindings(room_id, message_id, state) VALUES ('team', ?, 'queued')", otherMessageID); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("upgraded composite FK accepted cross-room binding: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	assertMigrationVersion(t, s, 2, 2)
	if got := tableCount(t, s, "messages"); got != 3 {
		t.Fatalf("idempotent reopen changed messages: %d", got)
	}
}

func TestFreshDatabaseAppliesEveryMigration(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	assertMigrationVersion(t, s, 2, 2)
	var roomVersion int
	if err := s.db.QueryRow("SELECT schema_version FROM rooms WHERE id = 'team'").Scan(&roomVersion); err != nil {
		t.Fatal(err)
	}
	if roomVersion != 2 {
		t.Fatalf("room schema version=%d", roomVersion)
	}
}

func TestOpenRejectsFutureGappedAndCorruptMigrationState(t *testing.T) {
	cases := []struct {
		name     string
		versions []int
	}{
		{"future", []int{1, 2, 3}},
		{"gap", []int{2}},
		{"recorded-v1-without-v1-schema", []int{1}},
		{"recorded-current-without-schema", []int{1, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "corrupt.db")
			createMigrationState(t, path, tc.versions)
			s, err := Open(context.Background(), path)
			if s != nil {
				_ = s.Close()
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("versions=%v err=%v", tc.versions, err)
			}
		})
	}
}

func TestVersionTwoMigrationFailsClosedOnCrossRoomVersionOneData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cross-room-v1.db")
	createVersionOneFixture(t, path)
	db := openRawSQLite(t, path)
	if _, err := db.Exec(`
		INSERT INTO rooms(id, display_name, host_id, project_path, execution_owner_uid, status, next_seq, schema_version)
		VALUES ('other', 'Other', 'host-2', '/srv/other', 2002, 'ready', 2, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO members(room_id, uid, username, added_at) VALUES ('other', 2002, 'bob', '2026-09-03T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`
		INSERT INTO messages(room_id, client_message_id, actor_uid, kind, body, payload_hash, state, accepted_seq, created_at)
		VALUES ('other', '00000000000000000000000000000003', 2002, 'prompt', 'wrong room', ?, 'queued', 1, '2026-09-03T00:00:02Z')`, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO turn_bindings(room_id, message_id, state) VALUES ('team', ?, 'queued')", messageID); err != nil {
		t.Fatalf("v1 fixture unexpectedly rejected cross-room binding: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(context.Background(), path)
	if s != nil {
		_ = s.Close()
	}
	if err == nil {
		t.Fatal("cross-room v1 corruption was silently migrated")
	}

	db = openRawSQLite(t, path)
	defer db.Close()
	var maxVersion, bindingCount int
	if err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&maxVersion); err != nil {
		t.Fatal(err)
	}
	if maxVersion != 1 {
		t.Fatalf("failed migration recorded version %d", maxVersion)
	}
	if err := db.QueryRow("SELECT count(*) FROM turn_bindings").Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if bindingCount != 2 {
		t.Fatalf("failed migration changed bindings: %d", bindingCount)
	}
}

func TestOpenRejectsCorruptMigrationTimestampWithoutLeakingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt-migration-time.db")
	createVersionOneFixture(t, path)
	db := openRawSQLite(t, path)
	const corruptValue = "secret-invalid-migration-time"
	if _, err := db.Exec("UPDATE schema_migrations SET applied_at = ?", corruptValue); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(context.Background(), path)
	if s != nil {
		_ = s.Close()
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), corruptValue) {
		t.Fatalf("migration timestamp leaked through error: %v", err)
	}
}

func TestOpenRejectsVersionTwoWithWrongCompositeForeignKeyDeleteAction(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{
			name: "turn-binding-must-cascade",
			old:  "FOREIGN KEY (room_id, message_id) REFERENCES messages(room_id, id) ON DELETE CASCADE",
			new:  "FOREIGN KEY (room_id, message_id) REFERENCES messages(room_id, id)",
		},
		{
			name: "recovery-command-must-cascade",
			old:  "FOREIGN KEY (room_id, recovery_message_id) REFERENCES messages(room_id, id) ON DELETE CASCADE",
			new:  "FOREIGN KEY (room_id, recovery_message_id) REFERENCES messages(room_id, id)",
		},
		{
			name: "recovery-event-must-cascade",
			old:  "FOREIGN KEY (room_id, recovery_message_id) REFERENCES recovery_results_v2(room_id, recovery_message_id) ON DELETE CASCADE",
			new:  "FOREIGN KEY (room_id, recovery_message_id) REFERENCES recovery_results_v2(room_id, recovery_message_id)",
		},
		{
			name: "recovery-target-must-not-cascade",
			old:  "FOREIGN KEY (room_id, target_message_id) REFERENCES messages(room_id, id)",
			new:  "FOREIGN KEY (room_id, target_message_id) REFERENCES messages(room_id, id) ON DELETE CASCADE",
		},
		{
			name: "room-event-link-must-not-cascade",
			old:  "FOREIGN KEY (room_id, seq) REFERENCES room_events(room_id, seq)",
			new:  "FOREIGN KEY (room_id, seq) REFERENCES room_events(room_id, seq) ON DELETE CASCADE",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wrong-delete-action.db")
			createVersionOneFixture(t, path)
			db := openRawSQLite(t, path)
			migration, err := schemaFiles.ReadFile("schema/002_room_integrity.sql")
			if err != nil {
				t.Fatal(err)
			}
			if count := strings.Count(string(migration), tc.old); count != 1 {
				t.Fatalf("migration fragment count=%d want=1", count)
			}
			corruptMigration := strings.Replace(string(migration), tc.old, tc.new, 1)
			if _, err := db.Exec(corruptMigration); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO schema_migrations(version, applied_at) VALUES (2, '2026-09-03T00:00:01Z')"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			s, err := Open(context.Background(), path)
			if s != nil {
				_ = s.Close()
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("wrong delete action was accepted: %v", err)
			}
			if err.Error() != "invalid store input: room-qualified foreign key is missing" {
				t.Fatalf("unstable or leaking schema error: %v", err)
			}
		})
	}
}

func TestVersionTwoCompositeForeignKeyDeleteActions(t *testing.T) {
	t.Run("turn binding cascades from message", func(t *testing.T) {
		s := openUpgradedVersionOneFixture(t)
		result, err := s.db.Exec(`
			INSERT INTO messages(room_id, client_message_id, actor_uid, kind, body, payload_hash, state, accepted_seq, created_at)
			VALUES ('team', '00000000000000000000000000000004', 1001, 'prompt', 'cascade me', ?, 'queued', 3, '2026-09-03T00:00:02Z')`, make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		messageID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("INSERT INTO turn_bindings(room_id, message_id, state) VALUES ('team', ?, 'queued')", messageID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("DELETE FROM messages WHERE id = ?", messageID); err != nil {
			t.Fatal(err)
		}
		assertRowCount(t, s.db, "SELECT count(*) FROM turn_bindings WHERE message_id = ?", 0, messageID)
	})

	t.Run("recovery result cascades from command message", func(t *testing.T) {
		s := openUpgradedVersionOneFixture(t)
		var recoveryMessageID int64
		if err := s.db.QueryRow("SELECT recovery_message_id FROM recovery_results").Scan(&recoveryMessageID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("DELETE FROM messages WHERE id = ?", recoveryMessageID); err != nil {
			t.Fatal(err)
		}
		assertRowCount(t, s.db, "SELECT count(*) FROM recovery_results", 0)
		assertRowCount(t, s.db, "SELECT count(*) FROM recovery_result_events", 0)
	})

	t.Run("recovery event cascades from recovery result", func(t *testing.T) {
		s := openUpgradedVersionOneFixture(t)
		if _, err := s.db.Exec("DELETE FROM recovery_results"); err != nil {
			t.Fatal(err)
		}
		assertRowCount(t, s.db, "SELECT count(*) FROM recovery_result_events", 0)
	})

	t.Run("target message and room event do not cascade", func(t *testing.T) {
		s := openUpgradedVersionOneFixture(t)
		var targetMessageID, eventSeq int64
		if err := s.db.QueryRow("SELECT target_message_id FROM recovery_results").Scan(&targetMessageID); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow("SELECT seq FROM recovery_result_events").Scan(&eventSeq); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("DELETE FROM messages WHERE id = ?", targetMessageID); err == nil {
			t.Fatal("recovery target message unexpectedly cascaded")
		}
		if _, err := s.db.Exec("DELETE FROM room_events WHERE room_id = 'team' AND seq = ?", eventSeq); err == nil {
			t.Fatal("recovery-linked room event unexpectedly cascaded")
		}
		assertRowCount(t, s.db, "SELECT count(*) FROM recovery_results", 1)
		assertRowCount(t, s.db, "SELECT count(*) FROM recovery_result_events", 1)
	})
}

func openUpgradedVersionOneFixture(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	createVersionOneFixture(t, path)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func assertRowCount(t *testing.T, db *sql.DB, query string, want int, arguments ...any) {
	t.Helper()
	var got int
	if err := db.QueryRow(query, arguments...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("row count=%d want=%d", got, want)
	}
}

type versionOneFixture struct {
	promptMessageID int64
}

func createVersionOneFixture(t *testing.T, path string) versionOneFixture {
	t.Helper()
	db := openRawSQLite(t, path)
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	schema, err := schemaFiles.ReadFile("schema/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO schema_migrations(version, applied_at) VALUES (1, '2026-09-03T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO rooms(id, display_name, host_id, project_path, execution_owner_uid, status, next_seq, schema_version)
		VALUES ('team', 'Team', 'host-1', '/srv/project', 1001, 'ready', 3, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO members(room_id, uid, username, added_at) VALUES ('team', 1001, 'alice', '2026-09-03T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	promptResult, err := db.Exec(`
		INSERT INTO messages(room_id, client_message_id, actor_uid, kind, body, payload_hash, state, accepted_seq, created_at)
		VALUES ('team', '00000000000000000000000000000001', 1001, 'prompt', 'persist me', ?, 'needs-review', 1, '2026-09-03T00:00:00Z')`, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	promptMessageID, err := promptResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO turn_bindings(room_id, message_id, state, error_code, error_digest) VALUES ('team', ?, 'needs-review', 'delivery-unknown', ?)", promptMessageID, digest); err != nil {
		t.Fatal(err)
	}
	recoveryResult, err := db.Exec(`
		INSERT INTO messages(room_id, client_message_id, actor_uid, kind, body, payload_hash, state, accepted_seq, created_at)
		VALUES ('team', '00000000000000000000000000000002', 1001, 'recover', '{}', ?, 'completed', 2, '2026-09-03T00:00:01Z')`, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	recoveryMessageID, err := recoveryResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO room_events(room_id, seq, actor_uid, kind, payload, created_at) VALUES ('team', 1, 1001, 'message/accepted', '{}', '2026-09-03T00:00:00Z'), ('team', 2, 1001, 'recovery/accepted', '{}', '2026-09-03T00:00:01Z')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO recovery_results(recovery_message_id, room_id, target_message_id, action) VALUES (?, 'team', ?, 'skip')", recoveryMessageID, promptMessageID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO recovery_result_events(recovery_message_id, ordinal, room_id, seq) VALUES (?, 0, 'team', 2)", recoveryMessageID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO runtime_checkpoints(room_id, generation, pid, pgid, process_start, codex_version, schema_sha256, process_state)
		VALUES ('team', 'generation-1', 0, 0, 'boot:42', 'codex-cli 0.151.0-alpha.7.2', ?, 'stopped')`, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO client_connections(id, room_id, member_uid, connected_at, last_ack_seq)
		VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'team', 1001, '2026-09-03T00:00:00Z', 2)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return versionOneFixture{promptMessageID: promptMessageID}
}

func createMigrationState(t *testing.T, path string, versions []int) {
	t.Helper()
	db := openRawSQLite(t, path)
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, version := range versions {
		if _, err := db.Exec("INSERT INTO schema_migrations(version, applied_at) VALUES (?, '2026-09-03T00:00:00Z')", version); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func openRawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db
}

func assertMigrationVersion(t *testing.T, s *Store, wantMax, wantCount int) {
	t.Helper()
	var maxVersion, count int
	if err := s.db.QueryRow("SELECT COALESCE(MAX(version), 0), count(*) FROM schema_migrations").Scan(&maxVersion, &count); err != nil {
		t.Fatal(err)
	}
	if maxVersion != wantMax || count != wantCount {
		t.Fatalf("migration max=%d count=%d want max=%d count=%d", maxVersion, count, wantMax, wantCount)
	}
}
