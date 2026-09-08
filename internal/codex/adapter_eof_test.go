package codex

import (
	"agent_romm/internal/room"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestAdapterDrainsAcceptedNotificationsAfterBackpressuredEOF(t *testing.T) {
	var wire strings.Builder
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&wire, "{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-1\",\"turnId\":\"turn-1\",\"itemId\":\"item-%d\",\"delta\":\"x\"}}\n", i)
	}
	rpc := NewRPCClient(strings.NewReader(wire.String()), io.Discard)
	adapter, err := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/project"})
	if err != nil {
		t.Fatal(err)
	}
	go rpc.Run(context.Background())
	select {
	case <-rpc.Done():
	case <-time.After(time.Second):
		t.Fatal("transport did not reach EOF")
	}
	count := 0
	for range adapter.Events() {
		count++
	}
	if count != 3 {
		t.Fatalf("accepted notifications lost at EOF: delivered %d of 3", count)
	}
}

func TestRuntimeDeliversAcceptedEOFEventsBeforeUnavailable(t *testing.T) {
	var wire strings.Builder
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&wire, "{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-1\",\"turnId\":\"turn-1\",\"itemId\":\"item-%d\",\"delta\":\"x\"}}\n", i)
	}
	rpc := NewRPCClient(strings.NewReader(wire.String()), io.Discard)
	adapter, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/project"})
	defer adapter.Close()
	go rpc.Run(context.Background())
	<-rpc.Done()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := &Runtime{ctx: ctx, events: make(chan room.AgentEvent, 1)}
	s := &Session{Adapter: adapter, Done: rpc.Done()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		failed, stop := r.forwardSession(s)
		if failed && !stop && r.drainFailedSession(s) {
			r.publish(room.AgentEvent{Kind: "runtime-unavailable"})
		}
	}()
	for i := 0; i < 4; i++ {
		select {
		case e := <-r.events:
			if i < 3 && (e.Kind != "item-delta" || string(e.ItemID) != fmt.Sprintf("item-%d", i+1)) || i == 3 && e.Kind != "runtime-unavailable" {
				t.Fatalf("out of order event %d: %+v", i, e)
			}
		case <-ctx.Done():
			t.Fatal("EOF drain stalled")
		}
	}
	<-done
}

func TestAdapterOwnerCancellationUnblocksStartupAndBackpressure(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprint(started), func(t *testing.T) {
			rpc := NewRPCClient(strings.NewReader(strings.Repeat("{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"thread-1\",\"turnId\":\"turn-1\",\"itemId\":\"item-1\",\"delta\":\"x\"}}\n", 3)), io.Discard)
			a, _ := NewAdapter(rpc, AdapterConfig{ProjectRoot: "/project"})
			if started {
				go rpc.Run(context.Background())
				<-rpc.Done()
			}
			done := make(chan struct{})
			go func() { a.Close(); a.Close(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("owner cancellation leaked adapter forwarder")
			}
		})
	}
}
