package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"agent_romm/internal/daemon"
	"agent_romm/internal/observability"
	"agent_romm/internal/room"
)

func TestCommittedLifecycleMetadataLoggedWithoutPayload(t *testing.T) {
	var out bytes.Buffer
	sink := loggedSink{daemon.NewHub(8, 4096), observability.New(&out)}
	for _, e := range []room.DurableEvent{
		{Seq: 1, Kind: "thread/bound", Payload: json.RawMessage(`{"thread_id":"thread-1","text":"SECRET"}`)},
		{Seq: 2, Kind: "turn/running", ActorUID: 1002, Payload: json.RawMessage(`{"turn_id":"turn-1","client_message_id":"00000000000000000000000000000001","text":"SECRET"}`)},
		{Seq: 3, Kind: "item/completed", Payload: json.RawMessage(`{"thread_id":"thread-1","turn_id":"turn-1","item_id":"item-1","payload":{"text":"SECRET"}}`)},
	} {
		sink.PublishDurable(e)
	}
	for _, want := range []string{"thread-1", "turn-1", "item-1", "agent-lifecycle", "1002"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "SECRET") {
		t.Fatal("raw output logged")
	}
}
