package store

import (
	"agent_romm/internal/room"
	"context"
	"database/sql"
	"errors"
)

func ValidRole(role string) bool { return role == "roommate" || role == "visitor" || role == "asker" }
func (s *Store) MemberRole(ctx context.Context, rid room.RoomID, uid room.UID) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(r.role,'roommate') FROM members m LEFT JOIN member_roles r ON r.room_id=m.room_id AND r.uid=m.uid WHERE m.room_id=? AND m.uid=?`, rid, uid).Scan(&role)
	return role, err
}
func (s *Store) DecideJoinRole(ctx context.Context, rid room.RoomID, id, action, role string) (JoinRequest, error) {
	if role == "" {
		return s.DecideJoin(ctx, rid, id, action)
	}
	if !ValidRole(role) || (action != "approve" && action != "role") {
		return JoinRequest{}, ErrAdmission
	}
	if action == "approve" {
		return s.decideJoin(ctx, rid, id, action, role)
	}
	var result JoinRequest
	err := s.transact(ctx, func(tx *sql.Tx) error {
		r, err := scanJoin(tx.QueryRowContext(ctx, "SELECT "+joinColumns+" FROM join_requests WHERE room_id=? AND id=? AND state='approved'", rid, id))
		if err != nil {
			return ErrAdmission
		}
		var owner room.UID
		if err = tx.QueryRowContext(ctx, "SELECT execution_owner_uid FROM rooms WHERE id=?", rid).Scan(&owner); err != nil {
			return err
		}
		if owner == r.UID {
			return errors.New("cannot change host role")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO member_roles(room_id,uid,role) VALUES(?,?,?) ON CONFLICT(room_id,uid) DO UPDATE SET role=excluded.role`, rid, r.UID, role)
		result = r
		result.Role = role
		return err
	})
	return result, err
}
