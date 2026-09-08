package daemon

import (
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"testing"
)

func TestReplayRefreshesActiveTurnAtSnapshot(t *testing.T) {
	calls := 0
	f := &fixture{snapshot: func() room.Snapshot {
		calls++
		if calls == 1 {
			return room.Snapshot{Active: &room.TurnBinding{TurnID: "old"}}
		}
		return room.Snapshot{ProjectionRevision: 3, LatestSeq: 12, Active: &room.TurnBinding{TurnID: "current"}}
	}}
	_, path := startServer(t, 1002, f)
	c := dial(t, path)
	request(t, c, "hello", protocol.Hello{MinVersion: 1, MaxVersion: 1})
	w, e := protocol.DecodeBody[protocol.Welcome](read(t, c).Body)
	if e != nil || w.ActiveTurnID != "old" {
		t.Fatal(w, e)
	}
	s, e := protocol.DecodeBody[protocol.RuntimeSnapshot](read(t, c).Body)
	if e != nil || s.ActiveTurnID != "current" || s.ProjectionRevision != 3 || s.DurableWatermark != 12 {
		t.Fatal(s, e)
	}
}
