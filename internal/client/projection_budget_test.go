package client

import (
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestLiveProjectionBoundsAccumulatedDeltas(t *testing.T) {
	p := projection{partial: make(map[itemKey]string)}
	for i := uint64(1); i <= 128; i++ {
		if err := p.transient(room.TransientEvent{Revision: i, Kind: "item-delta", ThreadID: "t", TurnID: "r", ItemID: "i", Delta: strings.Repeat("a", 64<<10)}, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, v := range p.partial {
		n += len(v)
	}
	if n > 2<<20 {
		t.Fatalf("unbounded live client accumulation %d", n)
	}
}

func TestTruncationSnapshotAndLiveStateAgreeUntilTurnEnds(t *testing.T) {
	live := projection{partial: make(map[itemKey]string)}
	var out bytes.Buffer
	_ = live.transient(room.TransientEvent{Revision: 1, Kind: "projection-truncated"}, &out)
	reconnect := projection{}
	reconnect.snapshot(protocol.RuntimeSnapshot{ProjectionRevision: 1, ProjectionTruncated: true})
	for _, p := range []*projection{&live, &reconnect} {
		_ = p.transient(room.TransientEvent{Revision: 2, Kind: "item-delta", TurnID: "turn-1", Delta: "late"}, io.Discard)
		if len(p.partial) != 0 || !p.truncated {
			t.Fatal("truncation state disagrees")
		}
		_ = p.durable(room.DurableEvent{Seq: 1, Kind: "turn/completed", Payload: json.RawMessage(`{"turn_id":"turn-1"}`)}, io.Discard)
		_ = p.transient(room.TransientEvent{Revision: 3, Kind: "item-delta", TurnID: "turn-2", Delta: "new"}, io.Discard)
		if p.truncated || len(p.partial) != 1 {
			t.Fatal("new turn remains truncated")
		}
	}
	if !strings.Contains(out.String(), "truncated") {
		t.Fatal("missing visible truncation")
	}
}

func TestSnapshotWatermarkKeepsCurrentTruncationAcrossOlderTerminal(t *testing.T) {
	p := projection{}
	p.snapshot(protocol.RuntimeSnapshot{ProjectionRevision: 8, DurableWatermark: 10, ActiveTurnID: "turn-2", ProjectionTruncated: true})
	_ = p.durable(room.DurableEvent{Seq: 9, Kind: "turn/completed", Payload: json.RawMessage(`{"turn_id":"turn-1"}`)}, io.Discard)
	if !p.truncated || p.active != "turn-2" {
		t.Fatal("older terminal invalidated authoritative truncation snapshot")
	}
}
