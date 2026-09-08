package store

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
)

func TestCompletedOutputAlwaysReplaysWithinFrameBudget(t *testing.T) {
	for _, text := range []string{strings.Repeat("<", 1536<<10), strings.Repeat("a", (8<<20)-32)} {
		t.Run(text[:1], func(t *testing.T) {
			s := openTestStore(t)
			seedRoom(t, s)
			ctx := context.Background()
			if _, err := s.BindThread(ctx, "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"}); err != nil {
				t.Fatal(err)
			}
			item := room.CompletedItem{ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Payload: json.RawMessage(`{"text":"` + text + `"}`)}
			e, err := s.RecordCompletedItem(ctx, "team", item)
			if err != nil {
				t.Fatal(err)
			}
			again, err := s.RecordCompletedItem(ctx, "team", item)
			if err != nil || again.Seq != e.Seq {
				t.Fatalf("history not idempotent: %v", err)
			}
			item.Payload = append([]byte(" \n"), item.Payload...)
			again, err = s.RecordCompletedItem(ctx, "team", item)
			if err != nil || again.Seq != e.Seq {
				t.Fatalf("formatted history not idempotent: %v", err)
			}
			events, err := s.Events(ctx, "team", e.Seq-1, e.Seq, 1)
			if err != nil || len(events) != 1 {
				t.Fatalf("replay: %v", err)
			}
			body, _ := json.Marshal(events[0])
			seq := uint64(e.Seq)
			var wire bytes.Buffer
			if err := protocol.NewWriter(&wire).Write(protocol.Envelope{Version: 1, Kind: protocol.KindEvent, Method: e.Kind, Seq: &seq, Body: body}); err != nil {
				t.Fatalf("persisted unreplayable item: %v", err)
			}
			if !bytes.Contains(e.Payload, []byte(`"originalBytes"`)) || !bytes.Contains(e.Payload, []byte(`"sha256"`)) || !bytes.Contains(e.Payload, []byte(`"output-omitted"`)) {
				t.Fatal("missing explicit omission evidence")
			}
		})
	}
}

func TestSmallHTMLOutputHistoryRemainsIdempotent(t *testing.T) {
	s := openTestStore(t)
	seedRoom(t, s)
	ctx := context.Background()
	_, _ = s.BindThread(ctx, "team", room.ThreadSnapshot{ID: "thread-1", CWD: "/srv/project"})
	item := room.CompletedItem{ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Payload: json.RawMessage(`{"text":"<ok>"}`)}
	e, err := s.RecordCompletedItem(ctx, "team", item)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.RecordCompletedItem(ctx, "team", item)
	if err != nil || again.Seq != e.Seq {
		t.Fatalf("equivalent HTML payload conflict: %v", err)
	}
	if bytes.Contains(e.Payload, []byte("output-omitted")) {
		t.Fatal("small output omitted")
	}
}
