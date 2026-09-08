package room

import "sort"

// LiveProjectionBudget leaves ample room in an 8 MiB reconnect frame. Charging
// six bytes per input byte covers HTML/control escaping before allocation.
const LiveProjectionBudget = 1 << 20

func LiveItemCost(thread, turn, item, partial string) int {
	return 256 + 6*(len(thread)+len(turn)+len(item)+len(partial))
}

type ProjectedItem struct {
	Partial   string
	Completed *CompletedItem
}
type ProjectionUpdate struct {
	Transient *TransientEvent
	Durable   *CompletedItem
}
type projectionKey struct {
	thread ThreadID
	turn   TurnID
	item   ItemID
}

// Projection is owned exclusively by the coordinator command loop.
type Projection struct {
	revision  uint64
	items     map[projectionKey]ProjectedItem
	used      int
	truncated bool
}

func NewProjection() *Projection { return &Projection{items: make(map[projectionKey]ProjectedItem)} }
func cloneCompleted(in *CompletedItem) *CompletedItem {
	if in == nil {
		return nil
	}
	out := *in
	out.Payload = append([]byte(nil), in.Payload...)
	return &out
}
func (p *Projection) Apply(event AgentEvent) ProjectionUpdate {
	switch event.Kind {
	case "item-delta":
		if p.truncated {
			return ProjectionUpdate{}
		}
		key := projectionKey{event.ThreadID, event.TurnID, event.ItemID}
		current, exists := p.items[key]
		// Once a complete replacement is known, delayed deltas cannot resurrect it.
		if current.Completed != nil {
			return ProjectionUpdate{}
		}
		cost := 6 * len(event.Delta)
		if !exists {
			cost += LiveItemCost(string(key.thread), string(key.turn), string(key.item), "")
		}
		if cost > LiveProjectionBudget-p.used {
			p.truncated = true
			p.revision++
			return ProjectionUpdate{Transient: &TransientEvent{Revision: p.revision, Kind: "projection-truncated", ThreadID: event.ThreadID, TurnID: event.TurnID}}
		}
		p.used += cost
		current.Partial += event.Delta
		p.items[key] = current
		p.revision++
		return ProjectionUpdate{Transient: &TransientEvent{Revision: p.revision, Kind: event.Kind, ThreadID: event.ThreadID, TurnID: event.TurnID, ItemID: event.ItemID, Delta: event.Delta}}
	case "item-completed":
		if event.Completed == nil {
			return ProjectionUpdate{}
		}
		completed := cloneCompleted(event.Completed)
		key := projectionKey{completed.ThreadID, completed.TurnID, completed.ItemID}
		if old, ok := p.items[key]; ok {
			p.used -= LiveItemCost(string(key.thread), string(key.turn), string(key.item), old.Partial)
			delete(p.items, key)
		}
		cost := LiveItemCost(string(key.thread), string(key.turn), string(key.item), "")
		wasTruncated := p.truncated
		if !p.truncated && cost <= LiveProjectionBudget-p.used {
			// Retain only the current turn's completion suppression key.
			p.items[key] = ProjectedItem{Completed: &CompletedItem{ThreadID: key.thread, TurnID: key.turn, ItemID: key.item}}
			p.used += cost
		} else {
			p.truncated = true
		}
		p.revision++
		if p.truncated && !wasTruncated {
			return ProjectionUpdate{Durable: completed, Transient: &TransientEvent{Revision: p.revision, Kind: "projection-truncated", ThreadID: key.thread, TurnID: key.turn}}
		}
		return ProjectionUpdate{Durable: cloneCompleted(completed), Transient: &TransientEvent{Revision: p.revision, Kind: event.Kind, ThreadID: completed.ThreadID, TurnID: completed.TurnID, ItemID: completed.ItemID}}
	}
	return ProjectionUpdate{}
}
func (p *Projection) Item(turn TurnID, item ItemID) ProjectedItem {
	for key, value := range p.items {
		if key.turn == turn && key.item == item {
			return ProjectedItem{Partial: value.Partial, Completed: cloneCompleted(value.Completed)}
		}
	}
	return ProjectedItem{}
}
func (p *Projection) Snapshot() (uint64, []LiveItemSnapshot) {
	items := make([]LiveItemSnapshot, 0)
	for key, value := range p.items {
		if value.Partial != "" {
			items = append(items, LiveItemSnapshot{ThreadID: key.thread, TurnID: key.turn, ItemID: key.item, Partial: value.Partial})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.ThreadID != b.ThreadID {
			return a.ThreadID < b.ThreadID
		}
		if a.TurnID != b.TurnID {
			return a.TurnID < b.TurnID
		}
		return a.ItemID < b.ItemID
	})
	return p.revision, items
}
func (p *Projection) reset() []TransientEvent {
	_, items := p.Snapshot()
	events := make([]TransientEvent, 0, len(items))
	for _, item := range items {
		p.revision++
		events = append(events, TransientEvent{Revision: p.revision, Kind: "item-removed", ThreadID: item.ThreadID, TurnID: item.TurnID, ItemID: item.ItemID})
	}
	p.items = make(map[projectionKey]ProjectedItem)
	p.used = 0
	if p.truncated {
		p.revision++
		events = append(events, TransientEvent{Revision: p.revision, Kind: "projection-reset"})
	}
	p.truncated = false
	return events
}

func (p *Projection) removeTurn(thread ThreadID, turn TurnID) []TransientEvent {
	_, items := p.Snapshot()
	var events []TransientEvent
	for _, item := range items {
		if item.ThreadID != thread || item.TurnID != turn {
			continue
		}
		p.revision++
		events = append(events, TransientEvent{Revision: p.revision, Kind: "item-removed", ThreadID: thread, TurnID: turn, ItemID: item.ItemID})
	}
	for key, value := range p.items {
		if key.thread == thread && key.turn == turn {
			p.used -= LiveItemCost(string(key.thread), string(key.turn), string(key.item), value.Partial)
			delete(p.items, key)
		}
	}
	if p.truncated {
		p.revision++
		events = append(events, TransientEvent{Revision: p.revision, Kind: "projection-reset", ThreadID: thread, TurnID: turn})
		p.truncated = false
	}
	return events
}
