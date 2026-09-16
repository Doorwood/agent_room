package daemon

import (
	"agent_romm/internal/room"
	"context"
)

// SubmitFromBot is called only after the Host connector validates its mapping.
func (s *Server) SubmitFromBot(ctx context.Context, m room.Member, id, text string) (room.Acceptance, error) {
	return s.deps.Coordinator.Submit(ctx, room.Actor{UID: m.UID, Name: m.Name}, room.SubmitInput{ClientMessageID: room.ClientMessageID(id), Text: text})
}
func (s *Server) BotSnapshot(ctx context.Context) (room.Snapshot, error) {
	return s.deps.Coordinator.Snapshot(ctx)
}
