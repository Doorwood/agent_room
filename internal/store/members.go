package store

import (
	"agent_romm/internal/room"
	"context"
)

// ListMembers includes offline and revoked identities retained for attribution.
func (s *Store) ListMembers(ctx context.Context, id room.RoomID) ([]room.Member, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT uid,username FROM members WHERE room_id=? ORDER BY uid", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []room.Member
	for rows.Next() {
		var member room.Member
		if err := rows.Scan(&member.UID, &member.Name); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}
