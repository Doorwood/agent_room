package store

import (
	"agent_romm/internal/room"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// The frontier is durable BEFORE turn/start. Unknown-delivery turns therefore
// remain hidden during recovery, including a crash before the turn ID reply.
func (s *Store) BeginPrivateTurn(ctx context.Context, rid room.RoomID, thread room.ThreadID, baseline []room.TurnID) error {
	b, err := json.Marshal(baseline)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO private_turn_frontiers(room_id,thread_id,baseline)VALUES(?,?,?)`, rid, thread, string(b))
	return err
}
func (s *Store) EndPrivateTurn(ctx context.Context, rid room.RoomID, thread room.ThreadID, turns []room.TurnID) error {
	return s.transact(ctx, func(tx *sql.Tx) error {
		for _, id := range turns {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO private_turns(room_id,thread_id,turn_id)VALUES(?,?,?)`, rid, thread, id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM private_turn_frontiers WHERE room_id=? AND thread_id=?`, rid, thread)
		return err
	})
}
func (s *Store) PrivateTurns(ctx context.Context, rid room.RoomID, thread room.ThreadID, turns []room.TurnSnapshot) (map[room.TurnID]bool, error) {
	hidden := map[room.TurnID]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT turn_id FROM private_turns WHERE room_id=? AND thread_id=?`, rid, thread)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id room.TurnID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		hidden[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var baseline string
	err = s.db.QueryRowContext(ctx, `SELECT baseline FROM private_turn_frontiers WHERE room_id=? AND thread_id=?`, rid, thread).Scan(&baseline)
	if errors.Is(err, sql.ErrNoRows) {
		return hidden, nil
	}
	if err != nil {
		return nil, err
	}
	var known []room.TurnID
	if err = json.Unmarshal([]byte(baseline), &known); err != nil {
		return nil, err
	}
	before := map[room.TurnID]bool{}
	for _, id := range known {
		before[id] = true
	}
	var added []room.TurnID
	for _, turn := range turns {
		if !before[turn.ID] {
			hidden[turn.ID] = true
			added = append(added, turn.ID)
			if turn.State == room.RequestRunning {
				return nil, errors.New("只读问答的结束状态尚未确认，暂停主任务恢复")
			}
		}
	}
	if err = s.EndPrivateTurn(ctx, rid, thread, added); err != nil {
		return nil, err
	}
	return hidden, nil
}
