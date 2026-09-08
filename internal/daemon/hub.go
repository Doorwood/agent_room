package daemon

import (
	"agent_romm/internal/room"
	"encoding/json"
	"sort"
	"sync"
)

type Broadcast struct {
	Durable   *room.DurableEvent
	Transient *room.TransientEvent
}
type Subscription interface {
	Events() <-chan Broadcast
	Closed() bool
	Close()
}
type Hub struct {
	mu                  sync.Mutex
	maxEvents, maxBytes int
	subs                map[*subscription]struct{}
}
type subscription struct {
	hub    *Hub
	actor  room.Actor
	events chan Broadcast
	done   chan struct{}
	closed bool
	sizes  []int
	bytes  int
}

func NewHub(maxEvents, maxBytes int) *Hub {
	if maxEvents <= 0 || maxEvents > 256 {
		maxEvents = 256
	}
	if maxBytes <= 0 || maxBytes > 16<<20 {
		maxBytes = 16 << 20
	}
	return &Hub{maxEvents: maxEvents, maxBytes: maxBytes, subs: make(map[*subscription]struct{})}
}
func (h *Hub) Subscribe(actor room.Actor) Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &subscription{hub: h, actor: actor, events: make(chan Broadcast, h.maxEvents), done: make(chan struct{})}
	h.subs[s] = struct{}{}
	return s
}
func (s *subscription) Events() <-chan Broadcast { return s.events }
func (s *subscription) Closed() bool             { s.hub.mu.Lock(); defer s.hub.mu.Unlock(); return s.closed }
func (s *subscription) Close()                   { s.hub.mu.Lock(); defer s.hub.mu.Unlock(); s.closeLocked() }
func (s *subscription) closeLocked() {
	if !s.closed {
		s.closed = true
		delete(s.hub.subs, s)
		close(s.done)
		close(s.events)
		s.sizes = nil
		s.bytes = 0
	}
}
func (h *Hub) PublishDurable(e room.DurableEvent)     { h.publish(Broadcast{Durable: &e}) }
func (h *Hub) PublishTransient(e room.TransientEvent) { h.publish(Broadcast{Transient: &e}) }
func (h *Hub) publish(b Broadcast) {
	encoded, err := json.Marshal(b)
	if err != nil {
		return
	}
	size := len(encoded)
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		consumed := len(s.sizes) - len(s.events)
		for _, n := range s.sizes[:consumed] {
			s.bytes -= n
		}
		s.sizes = s.sizes[consumed:]
		if len(s.events) >= h.maxEvents || s.bytes+size > h.maxBytes {
			s.closeLocked()
			continue
		}
		clone := b
		if b.Durable != nil {
			e := *b.Durable
			e.Payload = append([]byte(nil), e.Payload...)
			clone.Durable = &e
		}
		if b.Transient != nil {
			e := *b.Transient
			clone.Transient = &e
		}
		s.events <- clone
		s.sizes = append(s.sizes, size)
		s.bytes += size
	}
}
func (h *Hub) Members() []room.Actor {
	h.mu.Lock()
	defer h.mu.Unlock()
	unique := map[room.UID]room.Actor{}
	for s := range h.subs {
		unique[s.actor.UID] = s.actor
	}
	out := make([]room.Actor, 0, len(unique))
	for _, a := range unique {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out
}
