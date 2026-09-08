package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"agent_romm/internal/room"

	_ "modernc.org/sqlite"
)

var (
	ErrAlreadyInitialized  = errors.New("store is already initialized")
	ErrConnectionNotFound  = errors.New("connection not found")
	ErrCorruptStore        = errors.New("corrupt store data")
	ErrIdempotencyConflict = errors.New("idempotency key conflicts with stored command")
	ErrInvalidInput        = errors.New("invalid store input")
	ErrInvalidState        = errors.New("invalid state transition")
	ErrMemberNotFound      = errors.New("room member not found")
	ErrMessageNotFound     = errors.New("message not found")
	ErrRoomNotFound        = errors.New("room not found")
	ErrStaleRecovery       = room.ErrStaleRecovery
)

type Store struct {
	db *sql.DB

	// beforeCommit is deliberately package-private. Same-package tests use it to
	// simulate a crash after every transaction write but before durability.
	beforeCommit func() error
}

func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: database path is blank", ErrInvalidInput)
	}
	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	closeOnError := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		return closeOnError(fmt.Errorf("ping sqlite: %w", err))
	}
	if err := verifyPragmas(ctx, db); err != nil {
		return closeOnError(err)
	}
	if err := migrate(ctx, db); err != nil {
		return closeOnError(err)
	}
	return &Store{db: db}, nil
}

func sqliteDSN(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: resolve database path: %v", ErrInvalidInput, err)
	}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "journal_mode(WAL)")
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: query.Encode()}
	return dsn.String(), nil
}

func verifyPragmas(ctx context.Context, db *sql.DB) error {
	var foreignKeys, busyTimeout int
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("verify sqlite foreign_keys: %w", err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("verify sqlite busy_timeout: %w", err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("verify sqlite journal_mode: %w", err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 || strings.ToLower(journalMode) != "wal" {
		return fmt.Errorf("sqlite pragmas not established: foreign_keys=%d busy_timeout=%d journal_mode=%q", foreignKeys, busyTimeout, journalMode)
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) transact(ctx context.Context, write func(*sql.Tx) error) error {
	return s.transactConditional(ctx, write, func() bool { return true })
}

func (s *Store) transactConditional(ctx context.Context, write func(*sql.Tx) error, shouldRunHook func() bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sqlite transaction: %w", err)
	}
	defer tx.Rollback()
	if err := write(tx); err != nil {
		return err
	}
	if shouldRunHook() && s.beforeCommit != nil {
		if err := s.beforeCommit(); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite transaction: %w", err)
	}
	return nil
}

func nowUTC() time.Time { return time.Now().UTC() }

func encodeTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func decodeTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}
