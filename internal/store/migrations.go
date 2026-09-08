package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
)

const schemaVersion = 5

var migrationFiles = []string{
	"schema/001_initial.sql",
	"schema/002_room_integrity.sql",
	"schema/003_roles.sql",
	"schema/004_private_turns.sql",
	"schema/005_project_tasks.sql",
}

//go:embed schema/*.sql
var schemaFiles embed.FS

type migrationQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func migrate(ctx context.Context, db *sql.DB) error {
	if len(migrationFiles) != schemaVersion {
		return fmt.Errorf("migration manifest does not match schema version")
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema migration table: %w", err)
	}

	applied, err := appliedMigrationCount(ctx, db)
	if err != nil {
		return err
	}
	if err := validateSchemaAtVersion(ctx, db, applied); err != nil {
		return err
	}
	for version := applied + 1; version <= schemaVersion; version++ {
		if err := applyMigration(ctx, db, version, migrationFiles[version-1]); err != nil {
			return err
		}
	}
	return nil
}

func appliedMigrationCount(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, "SELECT version, applied_at FROM schema_migrations ORDER BY version")
	if err != nil {
		return 0, fmt.Errorf("%w: read schema migrations", ErrInvalidInput)
	}
	defer rows.Close()

	expected := 1
	for rows.Next() {
		var version int
		var appliedAt string
		if err := rows.Scan(&version, &appliedAt); err != nil {
			return 0, fmt.Errorf("%w: read schema migration row", ErrInvalidInput)
		}
		if version > schemaVersion {
			return 0, fmt.Errorf("%w: database schema version is newer than supported", ErrInvalidInput)
		}
		if version != expected {
			return 0, fmt.Errorf("%w: schema migration sequence is not contiguous", ErrInvalidInput)
		}
		if _, err := decodeTime(appliedAt); err != nil {
			return 0, fmt.Errorf("%w: invalid schema migration timestamp", ErrInvalidInput)
		}
		expected++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("%w: iterate schema migrations", ErrInvalidInput)
	}
	return expected - 1, nil
}

func applyMigration(ctx context.Context, db *sql.DB, version int, name string) error {
	schema, err := schemaFiles.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read embedded schema version %d: %w", version, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema version %d: %w", version, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, string(schema)); err != nil {
		return fmt.Errorf("apply schema version %d: %w", version, err)
	}
	if err := validateSchemaAtVersion(ctx, tx, version); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)", version, encodeTime(nowUTC())); err != nil {
		return fmt.Errorf("record schema version %d: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema version %d: %w", version, err)
	}
	return nil
}

func validateSchemaAtVersion(ctx context.Context, db migrationQuerier, version int) error {
	const applicationTables = 9
	var tableCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_schema
		WHERE type = 'table' AND name IN (
			'rooms', 'members', 'messages', 'turn_bindings', 'client_connections',
			'room_events', 'runtime_checkpoints', 'recovery_results', 'recovery_result_events'
		)`).Scan(&tableCount); err != nil {
		return fmt.Errorf("%w: inspect application schema", ErrInvalidInput)
	}
	if version == 0 {
		if tableCount != 0 {
			return fmt.Errorf("%w: application schema exists without a migration record", ErrInvalidInput)
		}
		return nil
	}
	if tableCount != applicationTables {
		return fmt.Errorf("%w: schema version %d is incomplete", ErrInvalidInput, version)
	}

	var wrongRoomVersions int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM rooms WHERE schema_version <> ?", version).Scan(&wrongRoomVersions); err != nil {
		return fmt.Errorf("%w: inspect room schema versions", ErrInvalidInput)
	}
	if wrongRoomVersions != 0 {
		return fmt.Errorf("%w: room schema versions do not match migration state", ErrInvalidInput)
	}
	if err := validateForeignKeys(ctx, db); err != nil {
		return err
	}
	if version >= 5 {
		for _, table := range []string{"project_tasks", "task_messages", "task_changes", "task_receipts"} {
			var n int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return fmt.Errorf("missing task schema: %w", err)
			}
		}
	}
	if version >= 4 {
		for _, table := range []string{"private_turns", "private_turn_frontiers"} {
			var n int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return fmt.Errorf("missing private turn schema: %w", err)
			}
		}
	}
	if version >= 3 {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM member_roles").Scan(&n); err != nil {
			return fmt.Errorf("missing roles schema: %w", err)
		}
	}
	if version >= 2 {
		return validateVersionTwoSchema(ctx, db)
	}
	return nil
}

func validateForeignKeys(ctx context.Context, db migrationQuerier) error {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("%w: inspect foreign keys", ErrInvalidInput)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: foreign key integrity check failed", ErrInvalidInput)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: inspect foreign keys", ErrInvalidInput)
	}
	return nil
}

func validateVersionTwoSchema(ctx context.Context, db migrationQuerier) error {
	rows, err := db.QueryContext(ctx, "PRAGMA index_info('messages_room_id_id_unique')")
	if err != nil {
		return fmt.Errorf("%w: inspect message room key", ErrInvalidInput)
	}
	var columns []string
	for rows.Next() {
		var sequence, columnID int
		var column string
		if err := rows.Scan(&sequence, &columnID, &column); err != nil {
			rows.Close()
			return fmt.Errorf("%w: inspect message room key", ErrInvalidInput)
		}
		columns = append(columns, column)
	}
	closeErr := rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: inspect message room key", ErrInvalidInput)
	}
	if closeErr != nil {
		return fmt.Errorf("%w: inspect message room key", ErrInvalidInput)
	}
	if len(columns) != 2 || columns[0] != "room_id" || columns[1] != "id" {
		return fmt.Errorf("%w: message room key is missing", ErrInvalidInput)
	}

	checks := []struct {
		table        string
		parent       string
		pairs        map[string]string
		deleteAction string
	}{
		{"turn_bindings", "messages", map[string]string{"room_id": "room_id", "message_id": "id"}, "CASCADE"},
		{"recovery_results", "messages", map[string]string{"room_id": "room_id", "recovery_message_id": "id"}, "CASCADE"},
		{"recovery_results", "messages", map[string]string{"room_id": "room_id", "target_message_id": "id"}, "NO ACTION"},
		{"recovery_result_events", "recovery_results", map[string]string{"room_id": "room_id", "recovery_message_id": "recovery_message_id"}, "CASCADE"},
		{"recovery_result_events", "room_events", map[string]string{"room_id": "room_id", "seq": "seq"}, "NO ACTION"},
	}
	for _, check := range checks {
		ok, err := hasCompositeForeignKey(ctx, db, check.table, check.parent, check.pairs, check.deleteAction)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: room-qualified foreign key is missing", ErrInvalidInput)
		}
	}
	return nil
}

func hasCompositeForeignKey(ctx context.Context, db migrationQuerier, table, parent string, want map[string]string, deleteAction string) (bool, error) {
	query := fmt.Sprintf(`SELECT id, seq, "table", "from", "to", on_delete FROM pragma_foreign_key_list('%s') ORDER BY id, seq`, table)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return false, fmt.Errorf("%w: inspect room-qualified foreign keys", ErrInvalidInput)
	}
	defer rows.Close()
	type foreignKey struct {
		parent       string
		pairs        map[string]string
		deleteAction string
	}
	foreignKeys := make(map[int]foreignKey)
	for rows.Next() {
		var id, sequence int
		var parentTable, from, to, onDelete string
		if err := rows.Scan(&id, &sequence, &parentTable, &from, &to, &onDelete); err != nil {
			return false, fmt.Errorf("%w: inspect room-qualified foreign keys", ErrInvalidInput)
		}
		key := foreignKeys[id]
		if key.pairs == nil {
			key = foreignKey{parent: parentTable, pairs: make(map[string]string), deleteAction: onDelete}
		} else if key.deleteAction != onDelete {
			key.deleteAction = ""
		}
		key.pairs[from] = to
		foreignKeys[id] = key
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("%w: inspect room-qualified foreign keys", ErrInvalidInput)
	}
	for _, key := range foreignKeys {
		if key.parent != parent || key.deleteAction != deleteAction || len(key.pairs) != len(want) {
			continue
		}
		matched := true
		for from, to := range want {
			if key.pairs[from] != to {
				matched = false
				break
			}
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}
