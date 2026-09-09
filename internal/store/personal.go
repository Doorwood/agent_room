package store

import (
	"agent_romm/internal/room"
	"context"
	"errors"
)

func (s *Store) CheckPersonalSender(ctx context.Context, rid room.RoomID, uid room.UID, id room.ClientMessageID) error {
	role, err := s.MemberRole(ctx, rid, uid)
	if err != nil || role != "roommate" {
		return errors.New("发送者已失去协作权限")
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE room_id=? AND client_message_id=? AND actor_uid=? AND kind IN ('prompt','recovery-prompt') AND state IN ('dispatching','running')`, rid, id, uid).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("原发送者的工作轮次已结束，创建凭证无效")
	}
	return nil
}

func (s *Store) HasActiveWork(ctx context.Context, rid room.RoomID) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE room_id=? AND state IN ('dispatching','running')`, rid).Scan(&n)
	return n > 0, err
}

func (s *Store) ProjectRoot(ctx context.Context, rid room.RoomID) (string, error) {
	var root string
	err := s.db.QueryRowContext(ctx, `SELECT project_path FROM rooms WHERE id=?`, rid).Scan(&root)
	return root, err
}
