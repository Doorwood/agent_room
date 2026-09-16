package network

import (
	"agent_romm/internal/room"
	"context"
)

// WithBotMember serializes admission/role checks with network revocation.
// It is only available to the trusted local Host connector, never a TCP action.
func (s *Server) WithBotMember(ctx context.Context, uid room.UID, work bool, fn func(room.Member) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.store.BotMember(ctx, s.room, uid, work)
	if err != nil {
		return err
	}
	return fn(m)
}
