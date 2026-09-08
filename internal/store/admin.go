package store

import (
	"agent_romm/internal/room"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// RepairThread atomically records the old binding, the reviewed replacement,
// and the reason. Recovery must still reconcile pending work before ready.
func (s *Store) RepairThread(ctx context.Context, id room.RoomID, snapshot room.ThreadSnapshot, reason room.RepairReason) (room.DurableEvent, error) {
	if e := validateRoomID(id); e != nil {
		return room.DurableEvent{}, e
	}
	if e := validateThreadSnapshot(snapshot); e != nil {
		return room.DurableEvent{}, e
	}
	if e := validateReason(reason.Code, reason.DetailDigest); e != nil {
		return room.DurableEvent{}, e
	}
	var event room.DurableEvent
	e := s.transact(ctx, func(tx *sql.Tx) error {
		var project string
		var old sql.NullString
		if e := tx.QueryRowContext(ctx, "SELECT project_path, codex_thread_id FROM rooms WHERE id = ?", id).Scan(&project, &old); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrRoomNotFound
			}
			return e
		}
		if snapshot.CWD != project {
			return fmt.Errorf("%w: repair cwd mismatch", ErrInvalidInput)
		}
		if _, e := tx.ExecContext(ctx, "UPDATE rooms SET codex_thread_id = ?, status = ? WHERE id = ?", snapshot.ID, room.RoomRecovering, id); e != nil {
			return e
		}
		var e error
		event, e = appendEvent(ctx, tx, id, 0, "thread/repaired", struct {
			PreviousThreadID string        `json:"previous_thread_id,omitempty"`
			ThreadID         room.ThreadID `json:"thread_id"`
			CWD              string        `json:"cwd"`
			Code             string        `json:"code"`
			DetailDigest     string        `json:"detail_digest"`
		}{old.String, snapshot.ID, snapshot.CWD, reason.Code, reason.DetailDigest})
		return e
	})
	return event, e
}
