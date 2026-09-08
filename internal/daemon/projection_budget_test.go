package daemon

import (
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"fmt"
	"strings"
	"testing"
)

func TestReconnectProjectionFitsFrameAndReportsTruncation(t *testing.T) {
	p := room.NewProjection()
	truncated := false
	for i := 0; i < 100; i++ {
		u := p.Apply(room.AgentEvent{Kind: "item-delta", ThreadID: "thread-1", TurnID: "turn-1", ItemID: room.ItemID(fmt.Sprint(i)), Delta: strings.Repeat("<", 32<<10)})
		truncated = truncated || u.Transient != nil && u.Transient.Kind == "projection-truncated"
	}
	revision, items := p.Snapshot()
	f := &fixture{snapshot: func() room.Snapshot {
		return room.Snapshot{ProjectionRevision: revision, ProjectionTruncated: truncated, LiveItems: items}
	}}
	_, path := startServer(t, 1002, f)
	c := dial(t, path)
	request(t, c, "hello", protocol.Hello{MinVersion: 1, MaxVersion: 1})
	read(t, c)
	frame := read(t, c)
	snap, err := protocol.DecodeBody[protocol.RuntimeSnapshot](frame.Body)
	if err != nil || !snap.ProjectionTruncated || snap.ProjectionRevision != revision || len(snap.LiveItems) != len(items) {
		t.Fatalf("invalid reconnect truncation snapshot: %v", err)
	}
	if len(frame.Body) > 2<<20 {
		t.Fatalf("oversized reconnect frame body: %d", len(frame.Body))
	}
}
