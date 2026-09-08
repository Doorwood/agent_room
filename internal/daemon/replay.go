package daemon

import (
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"context"
	"errors"
)

func sendBroadcast(w sessionWriter, b Broadcast) error {
	if b.Durable != nil {
		seq := uint64(b.Durable.Seq)
		return w.send(protocol.KindEvent, "", b.Durable.Kind, b.Durable, &seq)
	}
	if b.Transient != nil {
		return w.send(protocol.KindEvent, "", b.Transient.Kind, b.Transient, nil)
	}
	return nil
}
func (s *Server) replay(parent context.Context, w sessionWriter, helloID string, last room.Seq, sub *subscription) (uint64, error) {
	ctx, cancel := context.WithTimeout(parent, s.cfg.RequestTimeout)
	defer cancel()
	high, err := s.deps.Events.LatestSeq(ctx, s.cfg.RoomID)
	if err != nil {
		return 0, err
	}
	if last > high {
		return 0, errors.New("invalid replay cursor")
	}
	initial, err := s.deps.Coordinator.Snapshot(ctx)
	if err != nil {
		return 0, err
	}
	welcome := protocol.Welcome{RoomID: string(s.cfg.RoomID), RoomName: s.cfg.RoomName, ProjectRoot: s.cfg.ProjectRoot, ExecutionOwner: s.cfg.ExecutionOwner, FullOwnerAccess: true, LatestSeq: uint64(high)}
	if initial.Active != nil {
		welcome.ActiveTurnID = string(initial.Active.TurnID)
	}
	if err = w.send(protocol.KindResponse, helloID, "welcome", welcome, nil); err != nil {
		return 0, err
	}
	for last < high {
		if sub.Closed() {
			return 0, errors.New("slow subscriber")
		}
		events, err := s.deps.Events.Events(ctx, s.cfg.RoomID, last, high, 128)
		if err != nil {
			return 0, err
		}
		if len(events) == 0 {
			return 0, errors.New("incomplete replay")
		}
		for _, e := range events {
			if e.Seq <= last || e.Seq > high {
				return 0, errors.New("invalid replay order")
			}
			if err = sendBroadcast(w, Broadcast{Durable: &e}); err != nil {
				return 0, err
			}
			last = e.Seq
		}
	}
	snap, err := s.deps.Coordinator.Snapshot(ctx)
	if err != nil {
		return 0, err
	}
	out := protocol.RuntimeSnapshot{ProjectionRevision: snap.ProjectionRevision, DurableWatermark: uint64(snap.LatestSeq), LiveItems: make([]protocol.LiveItem, 0, len(snap.LiveItems))}
	out.ProjectionTruncated = snap.ProjectionTruncated
	if snap.Active != nil {
		out.ActiveTurnID = string(snap.Active.TurnID)
	}
	for _, item := range snap.LiveItems {
		out.LiveItems = append(out.LiveItems, protocol.LiveItem{ThreadID: string(item.ThreadID), TurnID: string(item.TurnID), ItemID: string(item.ItemID), Partial: item.Partial})
	}
	return snap.ProjectionRevision, w.send(protocol.KindEvent, "", "runtime-snapshot", out, nil)
}
