package room

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestProjectionAggregateBoundAndTerminalEviction(t *testing.T) {
	p := NewProjection()
	marked := false
	for i := 0; i < 128; i++ {
		u := p.Apply(AgentEvent{Kind: "item-delta", ThreadID: "thread-1", TurnID: "turn-1", ItemID: ItemID(fmt.Sprint(i)), Delta: strings.Repeat("<", 32<<10)})
		marked = marked || u.Transient != nil && u.Transient.Kind == "projection-truncated"
	}
	_, items := p.Snapshot()
	encoded, _ := json.Marshal(items)
	if len(encoded) > 2<<20 || !marked {
		t.Fatalf("unbounded snapshot bytes=%d explicit marker=%v", len(encoded), marked)
	}
	p.removeTurn("thread-1", "turn-1")
	for i := 0; i < 100; i++ {
		item := CompletedItem{ThreadID: "thread-1", TurnID: "turn-2", ItemID: ItemID(fmt.Sprint(i)), Payload: json.RawMessage(`{"text":"done"}`)}
		p.Apply(AgentEvent{Kind: "item-completed", Completed: &item})
	}
	p.removeTurn("thread-1", "turn-2")
	if len(p.items) != 0 {
		t.Fatalf("terminal retained %d completed items", len(p.items))
	}
}

func TestProjectionCompletedDoesNotRetainPayload(t *testing.T) {
	p := NewProjection()
	item := CompletedItem{ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Payload: json.RawMessage(`{"text":"done"}`)}
	p.Apply(AgentEvent{Kind: "item-completed", Completed: &item})
	if got := p.Item("turn-1", "item-1").Completed; got == nil || len(got.Payload) != 0 {
		t.Fatal("completed payload retained instead of completion tombstone")
	}
	if u := p.Apply(AgentEvent{Kind: "item-delta", ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Delta: "late"}); u.Transient != nil {
		t.Fatal("late delta resurrected completed item")
	}
}
