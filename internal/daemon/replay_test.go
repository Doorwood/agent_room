package daemon

import (
	"agent_romm/internal/room"
	"bytes"
	"testing"
)

func TestSlowClientIsDroppedWithoutBlockingFastClient(t *testing.T) {
	h := NewHub(2, 1<<20)
	slow := h.Subscribe(room.Actor{UID: 1})
	fast := h.Subscribe(room.Actor{UID: 2})
	defer fast.Close()
	for i := 1; i <= 3; i++ {
		h.PublishDurable(room.DurableEvent{Seq: room.Seq(i), Payload: []byte(`{}`)})
		if got := (<-fast.Events()).Durable.Seq; got != room.Seq(i) {
			t.Fatal(got)
		}
	}
	if !slow.Closed() {
		t.Fatal("slow subscriber remained")
	}
}

func TestHubByteBudgetAndPayloadIsolation(t *testing.T) {
	h := NewHub(256, 300)
	s := h.Subscribe(room.Actor{UID: 1})
	h.PublishDurable(room.DurableEvent{Payload: append(append([]byte{'"'}, bytes.Repeat([]byte("x"), 400)...), '"')})
	if !s.Closed() {
		t.Fatal("byte budget ignored")
	}
	h = NewHub(256, 1<<20)
	a := h.Subscribe(room.Actor{UID: 2})
	b := h.Subscribe(room.Actor{UID: 2})
	defer a.Close()
	defer b.Close()
	payload := []byte(`{"text":"a"}`)
	h.PublishDurable(room.DurableEvent{Payload: payload})
	payload[9] = 'z'
	first := <-a.Events()
	first.Durable.Payload[9] = 'x'
	second := <-b.Events()
	if !bytes.Equal(second.Durable.Payload, []byte(`{"text":"a"}`)) {
		t.Fatal("payload alias")
	}
	if len(h.Members()) != 1 {
		t.Fatal("duplicate member")
	}
	a.Close()
	if len(h.Members()) != 1 {
		t.Fatal("member disappeared")
	}
	b.Close()
	if len(h.Members()) != 0 {
		t.Fatal("member remained")
	}
}
