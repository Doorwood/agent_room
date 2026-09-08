package store

import (
	"context"
	"database/sql"
	"errors"

	"agent_romm/internal/room"
)

const agentActivitySQL = `SELECT
 EXISTS(SELECT 1 FROM messages WHERE room_id=? AND kind<>'note') OR
 EXISTS(SELECT 1 FROM turn_bindings WHERE room_id=?) OR
 EXISTS(SELECT 1 FROM room_events WHERE room_id=? AND kind='item/completed')`

func (s *Store) HasAgentActivity(ctx context.Context, id room.RoomID) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, agentActivitySQL, id, id, id).Scan(&active)
	return active, err
}

// ResetUnusedThread is called under the host lock only after the pinned upstream
// has confirmed this specific thread is absent. It never discards accepted work,
// items, notes, membership or the local session ID; its audit event is durable.
func (s *Store) ResetUnusedThread(ctx context.Context, id room.RoomID, expected room.ThreadID) (reset bool, err error) {
	if expected == "" {
		return false, ErrInvalidInput
	}
	err = s.transact(ctx, func(tx *sql.Tx) error {
		var current sql.NullString
		if e := tx.QueryRowContext(ctx, "SELECT codex_thread_id FROM rooms WHERE id=?", id).Scan(&current); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrRoomNotFound
			}
			return e
		}
		if !current.Valid || current.String != string(expected) {
			return nil
		}
		var active bool
		if e := tx.QueryRowContext(ctx, agentActivitySQL, id, id, id).Scan(&active); e != nil {
			return e
		}
		if active {
			return nil
		}
		if _, e := tx.ExecContext(ctx, "UPDATE rooms SET codex_thread_id=NULL,status='ready' WHERE id=? AND codex_thread_id=?", id, expected); e != nil {
			return e
		}
		if _, e := appendEvent(ctx, tx, id, 0, "thread/empty-reset", struct {
			Previous room.ThreadID `json:"previous_thread_id"`
			Reason   string        `json:"reason"`
		}{expected, "upstream-missing-before-first-task"}); e != nil {
			return e
		}
		reset = true
		return nil
	})
	return reset, err
}
