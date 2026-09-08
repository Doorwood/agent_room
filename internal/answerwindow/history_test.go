package answerwindow

import (
	"agent_romm/internal/room"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestHistoryPagesAllEventsAndArchivedProgress(t *testing.T) {
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Room("history")
	for i := 1; i <= 260; i++ {
		if err := w.add(Answer{Seq: uint64(i), Role: "user", Kind: "prompt", ClientID: fmt.Sprint(i), Text: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.add(Answer{Seq: 1, Text: "duplicate"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Event(room.DurableEvent{Seq: 261, Kind: "turn/running", ActorUID: 1, Payload: json.RawMessage(`{"client_message_id":"1","turn_id":"t"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := w.Event(room.DurableEvent{Seq: 262, Kind: "turn/completed", Payload: json.RawMessage(`{"turn_id":"t"}`)}); err != nil {
		t.Fatal(err)
	}
	before := uint64(0)
	seen := map[uint64]bool{}
	for {
		w.mu.Lock()
		page, more, err := w.historyPage(before)
		w.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 50 {
			t.Fatal("unbounded page")
		}
		for _, a := range page {
			if seen[a.Seq] {
				t.Fatal("duplicate")
			}
			seen[a.Seq] = true
			if a.Seq == 1 && (a.Ack != "处理完成。" || a.Turn != "t") {
				t.Fatal(a)
			}
		}
		if !more {
			break
		}
		before = page[0].Seq
	}
	if len(seen) != 260 {
		t.Fatalf("lost history: %d", len(seen))
	}
	for _, cursor := range []string{"-1", "garbage", "0"} {
		out := httptest.NewRecorder()
		w.serve(out, httptest.NewRequest("GET", w.URL()+"history?before="+cursor, nil))
		if out.Code != 400 {
			t.Fatal(cursor, out.Code)
		}
	}
	w.Room("other")
	page, _, err := w.historyPage(0)
	if err != nil || len(page) != 0 {
		t.Fatal("room history leaked", err)
	}
}
